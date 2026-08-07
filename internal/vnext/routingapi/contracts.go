package routingapi

import (
	"context"

	vnextstore "github.com/LuTianTian001/JieShan/internal/vnext/store"
)

const APIPrefix = "/api/vnext/routing-profiles"

type Repository interface {
	ListRoutingProfiles(context.Context) ([]vnextstore.RoutingProfile, error)
	GetRoutingProfile(context.Context, int64) (vnextstore.RoutingProfile, error)
	GetDefaultRoutingProfile(context.Context) (vnextstore.RoutingProfile, error)
	CreateRoutingProfile(context.Context, string) (vnextstore.RoutingProfile, error)
	UpdateRoutingProfile(context.Context, int64, int64, string) (vnextstore.RoutingProfile, error)
	DeleteRoutingProfile(context.Context, int64, int64) error

	ListRoutingProfileRoutes(context.Context, int64) ([]vnextstore.RoutingProfileRoute, error)
	GetRoutingProfileRoute(context.Context, int64, int64) (vnextstore.RoutingProfileRoute, error)
	CreateRoutingProfileRoute(context.Context, int64, int64, vnextstore.RoutingProfileRouteWrite) (vnextstore.RoutingProfileRoute, error)
	UpdateRoutingProfileRoute(context.Context, int64, int64, int64, bool) (vnextstore.RoutingProfileRoute, error)
	ReplaceRoutingProfileRouteTargets(context.Context, int64, int64, int64, []int64) (vnextstore.RoutingProfileRoute, error)
	DeleteRoutingProfileRoute(context.Context, int64, int64, int64) error

	CreatePublishedModelCAS(context.Context, int64, int64, vnextstore.PublishedModelWrite, []int64) (vnextstore.PublishedModel, error)
	UpdatePublishedModel(context.Context, int64, vnextstore.PublishedModelUpdate) (vnextstore.PublishedModel, error)
	ReplacePublishedModelTargets(context.Context, int64, int64, []int64) (vnextstore.PublishedModel, error)
	DeletePublishedModel(context.Context, int64, int64) error
}

var _ Repository = (*vnextstore.Store)(nil)
