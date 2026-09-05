//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type updateServiceCacheStub struct {
	data string
}

func (s *updateServiceCacheStub) GetUpdateInfo(context.Context) (string, error) {
	if s.data == "" {
		return "", errors.New("cache miss")
	}
	return s.data, nil
}

func (s *updateServiceCacheStub) SetUpdateInfo(_ context.Context, data string, _ time.Duration) error {
	s.data = data
	return nil
}

type updateServiceGitHubClientStub struct {
	release        *GitHubRelease
	recentReleases []*GitHubRelease
	recentErr      error
	latestRepo     string
	recentRepo     string
}

func (s *updateServiceGitHubClientStub) FetchLatestRelease(_ context.Context, repo string) (*GitHubRelease, error) {
	s.latestRepo = repo
	return s.release, nil
}

func (s *updateServiceGitHubClientStub) FetchRecentReleases(_ context.Context, repo string, _ int) ([]*GitHubRelease, error) {
	s.recentRepo = repo
	return s.recentReleases, s.recentErr
}

func (s *updateServiceGitHubClientStub) DownloadFile(context.Context, string, string, int64) error {
	panic("DownloadFile should not be called when no update is available")
}

func (s *updateServiceGitHubClientStub) FetchChecksumFile(context.Context, string) ([]byte, error) {
	panic("FetchChecksumFile should not be called when no update is available")
}

func TestUpdateServicePerformUpdateNoUpdateReturnsSentinel(t *testing.T) {
	svc := NewUpdateService(
		&updateServiceCacheStub{},
		&updateServiceGitHubClientStub{
			release: &GitHubRelease{
				TagName: "v0.1.132",
				Name:    "v0.1.132",
			},
		},
		"0.1.132",
		"release",
	)

	err := svc.PerformUpdate(context.Background())

	require.Error(t, err)
	require.True(t, errors.Is(err, ErrNoUpdateAvailable))
	require.ErrorIs(t, err, ErrNoUpdateAvailable)
}

func TestParseVersionHandlesCustomReleaseSuffix(t *testing.T) {
	require.Equal(t, [3]int{0, 1, 160}, parseVersion("v0.1.160-custom.abc123"))
	require.Equal(t, [3]int{0, 1, 160}, parseVersion("custom-v0.1.160.abc123"))
	require.Equal(t, -1, compareVersions("0.1.160", "v0.1.160-custom.abc123"))
	require.Equal(t, -1, compareVersions("0.1.160-custom.old", "v0.1.160-custom.new"))
	require.Equal(t, 0, compareVersions("0.1.160-custom.abc123", "v0.1.160-custom.abc123"))
}

func newRollbackTestService(current string, releases []*GitHubRelease) *UpdateService {
	return NewUpdateService(
		&updateServiceCacheStub{},
		&updateServiceGitHubClientStub{recentReleases: releases},
		current,
		"release",
	)
}

func TestUpdateServiceListRollbackVersionsFiltersAndCaps(t *testing.T) {
	releases := []*GitHubRelease{
		{TagName: "v0.1.148", PublishedAt: "2026-07-09T00:00:00Z"},                       // newer than current: excluded
		{TagName: "v0.1.147", PublishedAt: "2026-07-08T00:00:00Z"},                       // current: excluded
		{TagName: "v0.1.146-rc1", PublishedAt: "2026-07-07T12:00:00Z", Prerelease: true}, // prerelease: excluded
		{TagName: "v0.1.146", PublishedAt: "2026-07-07T00:00:00Z"},
		{TagName: "v0.1.145", PublishedAt: "2026-07-06T00:00:00Z", Draft: true}, // draft: excluded
		{TagName: "v0.1.144", PublishedAt: "2026-07-05T00:00:00Z"},
		{TagName: "v0.1.144", PublishedAt: "2026-07-05T00:00:00Z"}, // duplicate: excluded
		{TagName: "v0.1.143", PublishedAt: "2026-07-04T00:00:00Z"},
		{TagName: "v0.1.142", PublishedAt: "2026-07-03T00:00:00Z"}, // beyond cap of 3: excluded
	}
	svc := newRollbackTestService("0.1.147", releases)

	versions, err := svc.ListRollbackVersions(context.Background())

	require.NoError(t, err)
	require.Len(t, versions, 3)
	require.Equal(t, "0.1.146", versions[0].Version)
	require.Equal(t, "0.1.144", versions[1].Version)
	require.Equal(t, "0.1.143", versions[2].Version)
}

func TestUpdateServiceListRollbackVersionsSortsUnorderedInput(t *testing.T) {
	releases := []*GitHubRelease{
		{TagName: "v0.1.144"},
		{TagName: "v0.1.146"},
		{TagName: "v0.1.145"},
	}
	svc := newRollbackTestService("0.1.147", releases)

	versions, err := svc.ListRollbackVersions(context.Background())

	require.NoError(t, err)
	require.Len(t, versions, 3)
	require.Equal(t, "0.1.146", versions[0].Version)
	require.Equal(t, "0.1.145", versions[1].Version)
	require.Equal(t, "0.1.144", versions[2].Version)
}

func TestUpdateServiceListRollbackVersionsEmptyWhenNoneOlder(t *testing.T) {
	releases := []*GitHubRelease{
		{TagName: "v0.1.147"},
		{TagName: "v0.1.148"},
	}
	svc := newRollbackTestService("0.1.147", releases)

	versions, err := svc.ListRollbackVersions(context.Background())

	require.NoError(t, err)
	require.Empty(t, versions)
}

func TestUpdateServiceListRollbackVersionsPropagatesFetchError(t *testing.T) {
	svc := NewUpdateService(
		&updateServiceCacheStub{},
		&updateServiceGitHubClientStub{recentErr: errors.New("github unavailable")},
		"0.1.147",
		"release",
	)

	_, err := svc.ListRollbackVersions(context.Background())

	require.Error(t, err)
	require.Contains(t, err.Error(), "github unavailable")
}

func TestUpdateServiceRollbackToVersionRejectsDisallowedTargets(t *testing.T) {
	releases := []*GitHubRelease{
		{TagName: "v0.1.148"},
		{TagName: "v0.1.147"},
		{TagName: "v0.1.146"},
		{TagName: "v0.1.145"},
		{TagName: "v0.1.144"},
		{TagName: "v0.1.143"},
		{TagName: "v0.1.142"},
	}
	svc := newRollbackTestService("0.1.147", releases)

	for _, target := range []string{
		"",         // empty
		"0.1.147",  // current version
		"v0.1.147", // current version with prefix
		"0.1.148",  // newer than current
		"0.1.142",  // older than the 3 most recent
		"9.9.9",    // nonexistent
	} {
		err := svc.RollbackToVersion(context.Background(), target)
		require.ErrorIs(t, err, ErrRollbackVersionNotAllowed, "target %q should be rejected", target)
	}
}

func TestUpdateServiceRollbackToVersionAcceptsVPrefix(t *testing.T) {
	// No platform asset in the release: the target passes the allowlist check
	// and fails later at asset lookup, proving the version itself was accepted.
	releases := []*GitHubRelease{
		{TagName: "v0.1.147"},
		{TagName: "v0.1.146"},
	}
	svc := newRollbackTestService("0.1.147", releases)

	err := svc.RollbackToVersion(context.Background(), "v0.1.146")

	require.Error(t, err)
	require.NotErrorIs(t, err, ErrRollbackVersionNotAllowed)
	require.Contains(t, err.Error(), "no compatible release found")
}

func TestUpdateServiceCustomBuildUsesCustomReleaseRepository(t *testing.T) {
	client := &updateServiceGitHubClientStub{
		release: &GitHubRelease{TagName: "v0.1.180-custom.next"},
	}
	svc := NewUpdateService(
		&updateServiceCacheStub{},
		client,
		"0.1.179-custom.a800c61a94a5",
		"custom",
	)

	info, err := svc.CheckUpdate(context.Background(), true)
	require.NoError(t, err)
	require.True(t, info.HasUpdate)
	require.Equal(t, "pincman/sub2api", client.latestRepo)
}

func TestUpdateServiceReleaseBuildUsesOfficialRepository(t *testing.T) {
	client := &updateServiceGitHubClientStub{
		release: &GitHubRelease{TagName: "v0.1.179"},
	}
	svc := NewUpdateService(
		&updateServiceCacheStub{},
		client,
		"0.1.179",
		"release",
	)

	_, err := svc.CheckUpdate(context.Background(), true)
	require.NoError(t, err)
	require.Equal(t, "Wei-Shaw/sub2api", client.latestRepo)
}

func TestCompareVersionsIgnoresBuildMetadata(t *testing.T) {
	require.Equal(t, [3]int{0, 1, 179}, parseVersion("0.1.179-custom.a800c61a94a5"))
	require.Equal(t, 0, compareVersions("0.1.179-custom.a800c61a94a5", "0.1.179"))
	require.Equal(t, -1, compareVersions("0.1.179-custom.a800c61a94a5", "0.1.180"))
}

func TestHasNewerReleaseCustomBuildComparesSameBaseTags(t *testing.T) {
	tests := []struct {
		name    string
		current string
		latest  string
		want    bool
	}{
		{
			name:    "different custom tag is newer",
			current: "0.1.179-custom.a800c61a94a5",
			latest:  "v0.1.179-custom.next123456789",
			want:    true,
		},
		{
			name:    "same custom tag is current",
			current: "0.1.179-custom.a800c61a94a5",
			latest:  "v0.1.179-custom.a800c61a94a5",
			want:    false,
		},
		{
			name:    "official tag cannot replace custom build",
			current: "0.1.179-custom.a800c61a94a5",
			latest:  "v0.1.179",
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, hasNewerRelease(tt.current, tt.latest, "custom"))
		})
	}
}

func TestUpdateServiceCustomBuildUsesCustomTagFromCache(t *testing.T) {
	cache := &updateServiceCacheStub{
		data: fmt.Sprintf(
			`{"latest":"0.1.179-custom.next123456789","repository":"pincman/sub2api","timestamp":%d}`,
			time.Now().Unix(),
		),
	}
	svc := NewUpdateService(
		cache,
		&updateServiceGitHubClientStub{},
		"0.1.179-custom.a800c61a94a5",
		"custom",
	)

	info, err := svc.CheckUpdate(context.Background(), false)
	require.NoError(t, err)
	require.True(t, info.Cached)
	require.True(t, info.HasUpdate)
}

func TestUpdateServiceIgnoresCacheFromAnotherReleaseRepository(t *testing.T) {
	cache := &updateServiceCacheStub{
		data: fmt.Sprintf(
			`{"latest":"0.1.180","repository":"Wei-Shaw/sub2api","timestamp":%d}`,
			time.Now().Unix(),
		),
	}
	client := &updateServiceGitHubClientStub{
		release: &GitHubRelease{TagName: "v0.1.179-custom.next123456789"},
	}
	svc := NewUpdateService(
		cache,
		client,
		"0.1.179-custom.a800c61a94a5",
		"custom",
	)

	info, err := svc.CheckUpdate(context.Background(), false)
	require.NoError(t, err)
	require.False(t, info.Cached)
	require.Equal(t, "pincman/sub2api", client.latestRepo)
	require.True(t, info.HasUpdate)
}
