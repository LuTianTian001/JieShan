package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestRoutingProfileOverridesAreOrderedSubsetsWithDefaultFallback(t *testing.T) {
	s := newV3TestStore(t)
	ctx := context.Background()
	publishedID, targetIDs := createRoutingProfileTestRoute(t, s, "profile-model", "Alpha", "Beta", "Gamma")

	profileID, err := s.CreateRoutingProfile(ctx, "Fast lane")
	if err != nil {
		t.Fatal(err)
	}
	profile, err := s.GetRoutingProfile(ctx, profileID)
	if err != nil {
		t.Fatal(err)
	}
	fallback, err := s.GetRoutingProfileModelRoute(ctx, profileID, publishedID)
	if err != nil {
		t.Fatal(err)
	}
	if !fallback.InheritsDefault || routeSiteTargetIDs(fallback.Targets) != joinInt64s(targetIDs) {
		t.Fatalf("fallback route = %+v", fallback)
	}

	overrideIDs := []int64{targetIDs[2], targetIDs[0]}
	if err := s.SetRoutingProfileModelTargets(ctx, profileID, publishedID, profile.Revision, overrideIDs); err != nil {
		t.Fatal(err)
	}
	override, err := s.GetRoutingProfileModelRoute(ctx, profileID, publishedID)
	if err != nil {
		t.Fatal(err)
	}
	if override.InheritsDefault || routeSiteTargetIDs(override.Targets) != joinInt64s(overrideIDs) || override.ProfileRevision != profile.Revision+1 {
		t.Fatalf("override route = %+v", override)
	}
	resolved, err := s.ResolvePublishedModelForProfile(ctx, "profile-model", NowMS(), &profileID)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.RoutingProfileID == nil || *resolved.RoutingProfileID != profileID || resolved.RoutingProfileName != "Fast lane" || resolvedTargetIDs(resolved.Targets) != joinInt64s(overrideIDs) {
		t.Fatalf("resolved profile route = %+v", resolved)
	}
	defaultResolved, err := s.ResolvePublishedModel(ctx, "profile-model", NowMS())
	if err != nil {
		t.Fatal(err)
	}
	if defaultResolved.RoutingProfileID != nil || defaultResolved.RoutingProfileName != DefaultRoutingProfileName || resolvedTargetIDs(defaultResolved.Targets) != joinInt64s(targetIDs) {
		t.Fatalf("resolved default route = %+v", defaultResolved)
	}

	if err := s.ClearRoutingProfileModelTargets(ctx, profileID, publishedID, override.ProfileRevision); err != nil {
		t.Fatal(err)
	}
	cleared, err := s.GetRoutingProfileModelRoute(ctx, profileID, publishedID)
	if err != nil {
		t.Fatal(err)
	}
	if !cleared.InheritsDefault || routeSiteTargetIDs(cleared.Targets) != joinInt64s(targetIDs) {
		t.Fatalf("cleared route = %+v", cleared)
	}
	resolved, err = s.ResolvePublishedModelForProfile(ctx, "profile-model", NowMS(), &profileID)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.RoutingProfileID != nil || resolved.RoutingProfileName != DefaultRoutingProfileName {
		t.Fatalf("profile without model override did not fall back: %+v", resolved)
	}
}

func TestRoutingProfileWritesValidateTargetsAndRevisionAtomically(t *testing.T) {
	s := newV3TestStore(t)
	ctx := context.Background()
	publishedID, targetIDs := createRoutingProfileTestRoute(t, s, "primary-model", "One", "Two")
	otherPublishedID, otherTargetIDs := createRoutingProfileTestRoute(t, s, "other-model", "Other")
	if otherPublishedID == publishedID {
		t.Fatal("test setup reused published model")
	}
	profileID, err := s.CreateRoutingProfile(ctx, "Restricted")
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := s.GetRoutingProfile(ctx, profileID)

	if err := s.SetRoutingProfileModelTargets(ctx, profileID, publishedID, profile.Revision, []int64{targetIDs[0], targetIDs[0]}); err == nil {
		t.Fatal("duplicate target unexpectedly accepted")
	}
	if err := s.SetRoutingProfileModelTargets(ctx, profileID, publishedID, profile.Revision, []int64{targetIDs[0], otherTargetIDs[0]}); err == nil {
		t.Fatal("target from another published model unexpectedly accepted")
	}
	afterRejected, _ := s.GetRoutingProfile(ctx, profileID)
	if afterRejected.Revision != profile.Revision {
		t.Fatalf("rejected write changed profile revision: before=%d after=%d", profile.Revision, afterRejected.Revision)
	}
	if err := s.SetRoutingProfileModelTargets(ctx, profileID, publishedID, profile.Revision, []int64{targetIDs[1]}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRoutingProfileModelTargets(ctx, profileID, publishedID, profile.Revision, []int64{targetIDs[0]}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale profile revision error = %v", err)
	}
	if err := s.SetRoutingProfileModelTargets(ctx, profileID, publishedID, profile.Revision+1, nil); err == nil {
		t.Fatal("empty override unexpectedly accepted")
	}
}

func TestDeletingRoutingProfileReturnsDownstreamKeysToDefault(t *testing.T) {
	s := newV3TestStore(t)
	ctx := context.Background()
	profileID, err := s.CreateRoutingProfile(ctx, "Temporary")
	if err != nil {
		t.Fatal(err)
	}
	keyID, err := s.CreateDownstreamKey(ctx, DownstreamKeyWrite{
		Name: "Profile key", Enabled: true, RoutingProfileID: &profileID,
	}, "js_profile", "js_profile_secret")
	if err != nil {
		t.Fatal(err)
	}
	key, err := s.GetDownstreamKey(ctx, keyID)
	if err != nil || key.RoutingProfileID == nil || key.RoutingProfileName != "Temporary" {
		t.Fatalf("profile key = %+v, %v", key, err)
	}
	profile, _ := s.GetRoutingProfile(ctx, profileID)
	if err := s.DeleteRoutingProfile(ctx, profileID, profile.Revision); err != nil {
		t.Fatal(err)
	}
	key, err = s.GetDownstreamKey(ctx, keyID)
	if err != nil || key.RoutingProfileID != nil || key.RoutingProfileName != DefaultRoutingProfileName {
		t.Fatalf("key after profile delete = %+v, %v", key, err)
	}
}

func createRoutingProfileTestRoute(t *testing.T, s *Store, publicName string, siteNames ...string) (int64, []int64) {
	t.Helper()
	ctx := context.Background()
	type resource struct {
		siteID, endpointID, modelID int64
	}
	resources := make([]resource, 0, len(siteNames))
	for _, name := range siteNames {
		siteID, endpointID, _ := mustCreateSiteResources(t, s, publicName+name, 1)
		modelID, err := s.CreateSiteModel(ctx, SiteModelWrite{
			SiteID: siteID, EndpointID: endpointID, ModelName: "source-" + name, Enabled: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		resources = append(resources, resource{siteID: siteID, endpointID: endpointID, modelID: modelID})
	}
	publishedID, err := s.CreatePublishedModel(ctx, PublishedModelWrite{PublicName: publicName, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	targetIDs := make([]int64, 0, len(resources))
	for _, item := range resources {
		published, _ := s.GetPublishedModel(ctx, publishedID)
		targetID, err := s.CreateRouteSiteTarget(ctx, publishedID, published.Revision, RouteSiteTargetWrite{
			SiteID: item.siteID, EndpointID: item.endpointID, SiteModelID: item.modelID, Enabled: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		targetIDs = append(targetIDs, targetID)
	}
	return publishedID, targetIDs
}

func routeSiteTargetIDs(items []RouteSiteTarget) string {
	ids := make([]int64, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return joinInt64s(ids)
}

func resolvedTargetIDs(items []ResolvedRouteSiteTarget) string {
	ids := make([]int64, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return joinInt64s(ids)
}

func joinInt64s(items []int64) string {
	if len(items) == 0 {
		return ""
	}
	result := ""
	for index, item := range items {
		if index > 0 {
			result += ","
		}
		result += fmt.Sprintf("%d", item)
	}
	return result
}
