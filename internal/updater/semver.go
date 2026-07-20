package updater

import (
	"strconv"
	"strings"
)

type semanticVersion struct {
	major      int
	minor      int
	patch      int
	prerelease string
}

func compareVersions(a, b string) int {
	left := parseVersion(a)
	right := parseVersion(b)

	switch {
	case left.major != right.major:
		return compareInts(left.major, right.major)
	case left.minor != right.minor:
		return compareInts(left.minor, right.minor)
	case left.patch != right.patch:
		return compareInts(left.patch, right.patch)
	}

	if left.prerelease == right.prerelease {
		return 0
	}
	if left.prerelease == "" {
		return 1
	}
	if right.prerelease == "" {
		return -1
	}
	return comparePrerelease(left.prerelease, right.prerelease)
}

// comparePrerelease 按 semver 规则比较预发布标识符：
//   - 以点分段逐段比较；
//   - 全数字段按数值比较，否则字典序比较；数字段 < 非数字段；
//   - 段数少者居前（beta.2 < beta.2.1）。
//
// 修正了旧实现整串字典序导致的 beta.10 < beta.2 错误。
func comparePrerelease(a, b string) int {
	partsA := strings.Split(a, ".")
	partsB := strings.Split(b, ".")
	min := len(partsA)
	if len(partsB) < min {
		min = len(partsB)
	}
	for i := 0; i < min; i++ {
		pa := partsA[i]
		pb := partsB[i]
		aNum, aErr := strconv.Atoi(pa)
		bNum, bErr := strconv.Atoi(pb)
		switch {
		case aErr == nil && bErr == nil:
			if aNum != bNum {
				return compareInts(aNum, bNum)
			}
		case aErr == nil && bErr != nil:
			return -1 // numeric < non-numeric
		case aErr != nil && bErr == nil:
			return 1
		default:
			if pa != pb {
				if pa < pb {
					return -1
				}
				return 1
			}
		}
	}
	return compareInts(len(partsA), len(partsB))
}

func parseVersion(raw string) semanticVersion {
	clean := strings.TrimSpace(strings.TrimPrefix(raw, "v"))
	if clean == "" {
		return semanticVersion{}
	}

	var prerelease string
	if idx := strings.Index(clean, "-"); idx >= 0 {
		prerelease = clean[idx+1:]
		clean = clean[:idx]
	}

	parts := strings.Split(clean, ".")
	result := semanticVersion{prerelease: prerelease}
	if len(parts) > 0 {
		result.major = atoi(parts[0])
	}
	if len(parts) > 1 {
		result.minor = atoi(parts[1])
	}
	if len(parts) > 2 {
		result.patch = atoi(parts[2])
	}
	return result
}

func atoi(raw string) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0
	}
	return value
}

func compareInts(a, b int) int {
	switch {
	case a > b:
		return 1
	case a < b:
		return -1
	default:
		return 0
	}
}
