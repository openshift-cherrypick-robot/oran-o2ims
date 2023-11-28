/*
Copyright 2023 Red Hat Inc.

Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file except in
compliance with the License. You may obtain a copy of the License at

  http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software distributed under the License is
distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
implied. See the License for the specific language governing permissions and limitations under the
License.
*/

package service

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net/http"

	jsoniter "github.com/json-iterator/go"

	"github.com/openshift-kni/oran-o2ims/internal/data"
	"github.com/openshift-kni/oran-o2ims/internal/model"
	"github.com/openshift-kni/oran-o2ims/internal/search"
)

// ResourceHandlerBuilder contains the data and logic needed to create a new resource
// collection handler. Don't create instances of this type directly, use the NewResourceHandler
// function instead.
type ResourceHandlerBuilder struct {
	logger           *slog.Logger
	transportWrapper func(http.RoundTripper) http.RoundTripper
	cloudID          string
	backendURL       string
	backendToken     string
	graphqlQuery     string
	graphqlVars      *model.SearchInput
}

// ResourceHandler knows how to respond to requests to list resources. Don't create
// instances of this type directly, use the NewResourceHandler function instead.
type ResourceHandler struct {
	logger            *slog.Logger
	transportWrapper  func(http.RoundTripper) http.RoundTripper
	cloudID           string
	backendURL        string
	backendToken      string
	backendClient     *http.Client
	jsonAPI           jsoniter.API
	selectorEvaluator *search.SelectorEvaluator
	graphqlQuery      string
	graphqlVars       *model.SearchInput
	resourceFetcher   *ResourceFetcher
}

// NewResourceHandler creates a builder that can then be used to configure and create a
// handler for the collection of resources.
func NewResourceHandler() *ResourceHandlerBuilder {
	return &ResourceHandlerBuilder{}
}

// SetLogger sets the logger that the handler will use to write to the log. This is mandatory.
func (b *ResourceHandlerBuilder) SetLogger(
	value *slog.Logger) *ResourceHandlerBuilder {
	b.logger = value
	return b
}

// SetTransportWrapper sets the wrapper that will be used to configure the HTTP clients used to
// connect to other servers, including the backend server. This is optional.
func (b *ResourceHandlerBuilder) SetTransportWrapper(
	value func(http.RoundTripper) http.RoundTripper) *ResourceHandlerBuilder {
	b.transportWrapper = value
	return b
}

// SetCloudID sets the identifier of the O-Cloud of this handler. This is mandatory.
func (b *ResourceHandlerBuilder) SetCloudID(
	value string) *ResourceHandlerBuilder {
	b.cloudID = value
	return b
}

// SetBackendURL sets the URL of the backend server This is mandatory.
func (b *ResourceHandlerBuilder) SetBackendToken(
	value string) *ResourceHandlerBuilder {
	b.backendToken = value
	return b
}

// SetBackendToken sets the authentication token that will be used to authenticate to the backend
// server. This is mandatory.
func (b *ResourceHandlerBuilder) SetBackendURL(
	value string) *ResourceHandlerBuilder {
	b.backendURL = value
	return b
}

// SetGraphqlQuery sets the query to send to the search API server.
func (b *ResourceHandlerBuilder) SetGraphqlQuery(
	value string) *ResourceHandlerBuilder {
	b.graphqlQuery = value
	return b
}

// SetGraphqlVars sets the query vars to send to the search API server.
func (b *ResourceHandlerBuilder) SetGraphqlVars(
	value *model.SearchInput) *ResourceHandlerBuilder {
	b.graphqlVars = value
	return b
}

// Build uses the data stored in the builder to create and configure a new handler.
func (b *ResourceHandlerBuilder) Build() (
	result *ResourceHandler, err error) {
	// Check parameters:
	if b.logger == nil {
		err = errors.New("logger is mandatory")
		return
	}
	if b.cloudID == "" {
		err = errors.New("cloud identifier is mandatory")
		return
	}
	if b.backendURL == "" {
		err = errors.New("backend URL is mandatory")
		return
	}
	if b.backendToken == "" {
		err = errors.New("backend token is mandatory")
		return
	}

	// Create the HTTP client that we will use to connect to the backend:
	var backendTransport http.RoundTripper
	backendTransport = &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
	}
	if b.transportWrapper != nil {
		backendTransport = b.transportWrapper(backendTransport)
	}
	backendClient := &http.Client{
		Transport: backendTransport,
	}

	// Prepare the JSON iterator API:
	jsonConfig := jsoniter.Config{
		IndentionStep: 2,
	}
	jsonAPI := jsonConfig.Froze()

	// Create the filter expression evaluator:
	pathEvaluator, err := search.NewPathEvaluator().
		SetLogger(b.logger).
		Build()
	if err != nil {
		return
	}
	selectorEvaluator, err := search.NewSelectorEvaluator().
		SetLogger(b.logger).
		SetPathEvaluator(pathEvaluator.Evaluate).
		Build()
	if err != nil {
		return
	}

	// Create and populate the object:
	result = &ResourceHandler{
		logger:            b.logger,
		transportWrapper:  b.transportWrapper,
		cloudID:           b.cloudID,
		backendURL:        b.backendURL,
		backendToken:      b.backendToken,
		backendClient:     backendClient,
		selectorEvaluator: selectorEvaluator,
		jsonAPI:           jsonAPI,
		graphqlQuery:      b.graphqlQuery,
		graphqlVars:       b.graphqlVars,
	}
	return
}

// List is part of the implementation of the collection handler interface.
func (h *ResourceHandler) List(ctx context.Context,
	request *ListRequest) (response *ListResponse, err error) {
	// Transform the items into what we need:
	resources, err := h.fetchItems(ctx, request.ParentID)
	if err != nil {
		return
	}

	// Select only the items that satisfy the filter:
	if request.Selector != nil {
		resources = data.Select(
			resources,
			func(ctx context.Context, item data.Object) (result bool, err error) {
				result, err = h.selectorEvaluator.Evaluate(ctx, request.Selector, item)
				return
			},
		)
	}

	// Return the result:
	response = &ListResponse{
		Items: resources,
	}
	return
}

// Get is part of the implementation of the object handler interface.
func (h *ResourceHandler) Get(ctx context.Context,
	request *GetRequest) (response *GetResponse, err error) {
	h.resourceFetcher, err = NewResourceFetcher().
		SetLogger(h.logger).
		SetTransportWrapper(h.transportWrapper).
		SetCloudID(h.cloudID).
		SetBackendURL(h.backendURL).
		SetBackendToken(h.backendToken).
		SetGraphqlQuery(h.graphqlQuery).
		SetGraphqlVars(h.getObjectGraphqlVars(ctx, request.ID, request.ParentID)).
		Build()
	if err != nil {
		return
	}

	// Fetch the object:
	resource, err := h.fetchItem(ctx, request.ID, request.ParentID, h.resourceFetcher)
	if err != nil {
		return
	}

	// Return the result:
	response = &GetResponse{
		Object: resource,
	}
	return
}

func (h *ResourceHandler) fetchItems(
	ctx context.Context, parentID string) (result data.Stream, err error) {
	h.resourceFetcher, err = NewResourceFetcher().
		SetLogger(h.logger).
		SetTransportWrapper(h.transportWrapper).
		SetCloudID(h.cloudID).
		SetBackendURL(h.backendURL).
		SetBackendToken(h.backendToken).
		SetGraphqlQuery(h.graphqlQuery).
		SetGraphqlVars(h.getCollectionGraphqlVars(ctx, parentID)).
		Build()
	if err != nil {
		return
	}

	items, err := h.resourceFetcher.FetchItems(ctx)
	if err != nil {
		return
	}

	// Transform Items to Resources
	result = data.Map(items, h.mapItem)

	return
}

func (h *ResourceHandler) fetchItem(ctx context.Context,
	id, parentID string, resourceFetcher *ResourceFetcher) (resource data.Object, err error) {
	// Fetch items
	items, err := resourceFetcher.FetchItems(ctx)
	if err != nil {
		return
	}

	// Transform Items to Resources
	resources := data.Map(items, h.mapItem)

	// Get first result
	resource, err = resources.Next(ctx)

	return
}

func (h *ResourceHandler) getObjectGraphqlVars(ctx context.Context, id, parentId string) (graphqlVars *model.SearchInput) {
	graphqlVars = h.getCollectionGraphqlVars(ctx, parentId)

	// Filter results by resource ID
	graphqlVars.Filters = append(graphqlVars.Filters, &model.SearchFilter{
		Property: "_systemUUID",
		Values:   []*string{&id},
	})
	return
}

func (h *ResourceHandler) getCollectionGraphqlVars(ctx context.Context, id string) (graphqlVars *model.SearchInput) {
	graphqlVars = &model.SearchInput{}
	graphqlVars.Keywords = h.graphqlVars.Keywords
	graphqlVars.Filters = h.graphqlVars.Filters

	// Filter results by 'cluster' property
	// TODO: handle filtering for global hub search API when applicable
	graphqlVars.Filters = append(graphqlVars.Filters, &model.SearchFilter{
		Property: "cluster",
		Values:   []*string{&id},
	})

	// Filter results without '_systemUUID' property (could happen with stale objects)
	nonEmpty := "!"
	graphqlVars.Filters = append(graphqlVars.Filters, &model.SearchFilter{
		Property: "_systemUUID",
		Values:   []*string{&nonEmpty},
	})

	return
}

// Map an item to and O2 Resource object.
func (h *ResourceHandler) mapItem(ctx context.Context,
	from data.Object) (to data.Object, err error) {
	kind, err := data.GetString(from, "kind")
	if err != nil {
		return
	}

	switch kind {
	case KindNode:
		return h.mapNodeItem(ctx, from)
	}

	return
}

// Map a Node to an O2 Resource object.
func (h *ResourceHandler) mapNodeItem(ctx context.Context,
	from data.Object) (to data.Object, err error) {
	description, err := data.GetString(from, "name")
	if err != nil {
		return
	}

	resourcePoolID, err := data.GetString(from, "cluster")
	if err != nil {
		return
	}

	labels, err := data.GetString(from, "label")
	if err != nil {
		return
	}
	labelsMap := data.GetLabelsMap(labels)

	globalAssetID, err := data.GetString(from, "_uid")
	if err != nil {
		return
	}

	resourceID, err := data.GetString(from, "_systemUUID")
	if err != nil {
		return
	}

	resourceTypeID, err := h.resourceFetcher.GetResourceTypeID(from)
	if err != nil {
		return
	}

	to = data.Object{
		"resourceID":     resourceID,
		"resourceTypeID": resourceTypeID,
		"description":    description,
		"extensions":     labelsMap,
		"resourcePoolID": resourcePoolID,
		"globalAssetID":  globalAssetID,
	}
	return
}
