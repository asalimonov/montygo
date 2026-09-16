package docker

import (
	"fmt"
	"github.com/asalimonov/montygo/internal/buildinfo"
	"github.com/asalimonov/montygo/monterr"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// DefaultImage is the repository montygo pulls monty-server from.
const DefaultImage = "ghcr.io/asalimonov/monty-server"

// Variables that override the image of a Docker pool.
const (
	ImageEnv   = "MONTYGO_DOCKER_IMAGE"
	VersionEnv = "MONTYGO_DOCKER_VERSION"
)

var (
	releaseVersionRE = regexp.MustCompile(`^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$`)
	pseudoUntaggedRE = regexp.MustCompile(`^\d+\.0\.0-\d{14}-[0-9a-f]{12}$`)
	pseudoPreRE      = regexp.MustCompile(`^(\d+\.\d+\.\d+-[0-9A-Za-z.-]+)\.0\.\d{14}-[0-9a-f]{12}$`)
	pseudoPatchRE    = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)-0\.\d{14}-[0-9a-f]{12}$`)
	imageTagRE       = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	shortHashRE      = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
)

// imageCandidates resolves the references to try, in order. The option
// wins over the variable, which wins over the default; an explicit version or a
// pinned reference yields exactly one candidate.
func imageCandidates(image, version, binding string, getenv func(string) string) ([]string, error) {
	repo := firstNonEmpty(image, getenv(ImageEnv), DefaultImage)
	ver := firstNonEmpty(version, getenv(VersionEnv))
	if pinnedReference(repo) {
		if ver != "" {
			return nil, &monterr.OptionError{Message: fmt.Sprintf("image %q already pins a tag or digest; leave the version empty", repo)}
		}
		return []string{repo}, nil
	}
	if ver != "" {
		if !imageTagRE.MatchString(ver) {
			return nil, &monterr.OptionError{Message: fmt.Sprintf("invalid monty-server image tag %q", ver)}
		}
		return []string{repo + ":" + ver}, nil
	}
	tags := candidateTags(binding)
	if len(tags) == 0 {
		return nil, &monterr.OptionError{Message: fmt.Sprintf(
			"cannot derive a monty-server image tag from montygo version %q: set Options.Version or %s, "+
				"pin Options.Image or %s, or stamp -ldflags \"-X %s.buildVersion=<version>\"",
			binding, VersionEnv, ImageEnv, buildinfo.ModulePath)}
	}
	refs := make([]string, 0, len(tags))
	for _, tag := range tags {
		refs = append(refs, repo+":"+tag)
	}
	return refs, nil
}

// pinnedReference reports a reference that already names a digest or a tag.
func pinnedReference(ref string) bool {
	if strings.Contains(ref, "@") {
		return true
	}
	return strings.Contains(ref[strings.LastIndex(ref, "/")+1:], ":")
}

// candidateTags derives image tags from a binding version: the exact version
// first, then the release it was built from. Only releases are published, so a
// development tree falls back to its base release.
func candidateTags(v string) []string {
	if v == "" || v == "(devel)" || v == buildinfo.UnknownVersion || pseudoUntaggedRE.MatchString(v) {
		return nil
	}
	if m := pseudoPreRE.FindStringSubmatch(v); m != nil {
		return validTags(m[1])
	}
	if m := pseudoPatchRE.FindStringSubmatch(v); m != nil {
		patch, err := strconv.Atoi(m[3])
		if err != nil || patch == 0 {
			return nil
		}
		return validTags(fmt.Sprintf("%s.%s.%d", m[1], m[2], patch-1))
	}
	if base, ok := devBaseVersion(v); ok {
		return validTags(v, base)
	}
	if releaseVersionRE.MatchString(v) {
		return validTags(v)
	}
	return nil
}

// devBaseVersion strips the "-<short hash>" and "-dirty" suffixes scripts/version.sh
// appends. A prerelease whose last segment is hex is read as a hash.
func devBaseVersion(v string) (string, bool) {
	base := strings.TrimSuffix(v, "-dirty")
	dev := base != v
	if i := strings.LastIndex(base, "-"); i > 0 && shortHashRE.MatchString(base[i+1:]) {
		base, dev = base[:i], true
	}
	if !dev || !releaseVersionRE.MatchString(base) {
		return "", false
	}
	return base, true
}

func validTags(tags ...string) []string {
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		if !imageTagRE.MatchString(tag) || slices.Contains(out, tag) {
			continue
		}
		out = append(out, tag)
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
