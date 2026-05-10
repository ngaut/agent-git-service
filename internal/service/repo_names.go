package service

import (
	"fmt"
	"math/rand/v2"
	"strings"
)

// Curated word pools for generating evocative, URL-safe repository names.
// Format: adjective-noun (e.g., "amber-chronicle", "quiet-grove").
var repoNameAdjectives = []string{
	"amber", "ancient", "ashen", "cedar", "cobalt",
	"copper", "coral", "crystal", "curious", "deep",
	"distant", "drifting", "ember", "fading", "fleeting",
	"frost", "gentle", "golden", "hidden", "ivory",
	"jade", "lucid", "luminous", "lunar", "marble",
	"misty", "morning", "mossy", "opal", "patient",
	"quiet", "rust", "sage", "sapphire", "scarlet",
	"serene", "silent", "silver", "solar", "starlit",
	"tender", "twilight", "velvet", "verdant", "vivid",
	"wandering", "warm", "wistful",
}

var repoNameNouns = []string{
	"archive", "atlas", "beacon", "bloom", "bridge",
	"cairn", "canvas", "chronicle", "cipher", "compass",
	"cove", "delta", "drift", "echo", "forge",
	"fountain", "garden", "glen", "grove", "harbor",
	"haven", "hearth", "journal", "lantern", "leaf",
	"loom", "meadow", "mirror", "nest", "notebook",
	"oasis", "passage", "pebble", "prism", "quarry",
	"reef", "ridge", "scroll", "shell", "shore",
	"summit", "thread", "tower", "trail", "vale",
	"vessel", "voyage", "well", "whisper",
}

// GenerateRepoName produces a random, URL-safe poetic name like "amber-chronicle".
func GenerateRepoName() string {
	adj := repoNameAdjectives[rand.IntN(len(repoNameAdjectives))]
	noun := repoNameNouns[rand.IntN(len(repoNameNouns))]
	return adj + "-" + noun
}

// RepoDisplayName returns a locale-aware default display name for a new memory space.
func RepoDisplayName(locale string) string {
	lang := strings.ToLower(strings.SplitN(locale, "-", 2)[0])
	switch lang {
	case "zh":
		return "我的记忆空间"
	case "ja":
		return "マイメモリースペース"
	default:
		return "My Memory Space"
	}
}

// SequentialRepoName appends a numeric suffix to resolve name collisions.
// E.g., ("amber-chronicle", 2) → "amber-chronicle-2".
func SequentialRepoName(base string, n int) string {
	return fmt.Sprintf("%s-%d", base, n)
}
