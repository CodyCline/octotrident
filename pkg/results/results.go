package results

import (
	"sync"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

type Results struct {
	Mu                *sync.RWMutex
	Owner             string
	Name              string
	Repository        *git.Repository
	WorkingTree       *git.Worktree
	Remote            *git.Remote
	KnownCommits      map[string]plumbing.Hash
	KnownShortCommits map[string]plumbing.Hash
	ForkURLs          []string
	HiddenCommits     []plumbing.Hash
	//Non-existing commits that were attempted
	InvalidCommits []string
	BruteCommits   []string
}

func (rm *Results) UpsertCommit(hash plumbing.Hash) bool {
	rm.Mu.Lock()
	defer rm.Mu.Unlock()
	commit := hash.String()
	if _, exists := (rm.KnownCommits)[commit]; !exists {
		shortCommit := firstN(commit, 4)
		(rm.KnownCommits)[commit] = hash
		(rm.KnownShortCommits)[shortCommit] = hash
		return false
	}
	return true
}

// func (rm *Results) UpsertFork(url string) {
// 	rm.Mu.Lock()
// 	defer rm.Mu.Unlock()
// 	if _, exists := (rm.ForkURLs)[url]; !exists {
// 		(rm.ForkURLs)[url] = url
// 	}
// }

func firstN(str string, n int) string {
	v := []rune(str)
	if n >= len(v) {
		return str
	}
	return string(v[:n])
}

func (r *Results) hasSHACollision(hash string) bool {
	r.Mu.RLock()
	defer r.Mu.RUnlock()
	if _, exists := (r.KnownShortCommits)[hash]; exists {
		return true
	}
	return false

}

func (r *Results) GenerateShortSHAStrings(charLen int) {
	hexDigits := "0123456789abcdef"
	var generateCombinations func(prefix string, length int)

	generateCombinations = func(prefix string, length int) {
		if length == 0 {
			if !r.hasSHACollision(prefix) {
				r.BruteCommits = append(r.BruteCommits, prefix)
			}
			return
		}
		for _, digit := range hexDigits {
			generateCombinations(prefix+string(digit), length-1)
		}
	}
	generateCombinations("", charLen)
}
