package main

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"strconv"

	"github.com/google/go-github/v63/github"
	"github.com/joho/godotenv"
	"github.com/peterhellberg/link"
	"gopkg.in/src-d/go-git.v4"
	"gopkg.in/src-d/go-git.v4/plumbing"
	"gopkg.in/src-d/go-git.v4/plumbing/object"
)

type RepoMap struct {
	KnownCommits      map[string]bool
	KnownShortCommits map[string]bool
	Forks             map[string]string
	Total             int
}

func main() {
	// Replace with the URL of the GitHub repository you want to clone
	repoURL := "https://github.com/netlify/build-plugin-template"
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}
	// Create a temporary directory
	commitMap := RepoMap{
		Total:             0,
		Forks:             make(map[string]string),
		KnownShortCommits: make(map[string]bool),
		KnownCommits:      make(map[string]bool),
	}

	cloneRepository(repoURL, &commitMap)
	// Create a temporary directory
	// Clean up after the program ends
	apiKey := os.Getenv("GITHUB_PAT")
	client := github.NewClient(nil).WithAuthToken(apiKey)
	if err := resolveForks(client, repoURL, 1, &commitMap); err != nil {
		fmt.Println(err)
	}
	fmt.Println(len(commitMap.KnownCommits))

	// fmt.Println(len(commitMap.Forks))
	for _, fork := range commitMap.Forks {
		cloneRepository(fork, &commitMap)
	}
	fmt.Println(len(commitMap.KnownCommits))
	//Print out the commit hashes
	// for commitHash := range commitMap {
	// 	fmt.Printf("https://api.github.com/repos/netlify/build-plugin-template/commits/%s\n", commitHash)
	// }
}

func (rm *RepoMap) upsert(hash plumbing.Hash) {
	commit := hash.String()
	if _, exists := (rm.KnownCommits)[commit]; !exists {
		//Add seen commits to hashmap
		rm.Total++
		(rm.KnownCommits)[commit] = true
	}

}

func (rm *RepoMap) upsertFork(url string) {
	if _, exists := (rm.Forks)[url]; !exists {
		//Add seen commits to hashmap
		rm.Total++
		(rm.Forks)[url] = url
	}

}

func (rm *RepoMap) upsertShortSha(hash string) {
	if _, exists := (rm.KnownShortCommits)[hash]; !exists {
		//Add seen commits to hashmap
		rm.Total++
		(rm.Forks)[hash] = hash
	}

}

func resolveKnownCommits() {

}

func resolveForks(client *github.Client, uri string, page int, repoMap *RepoMap) error {
	ctx := context.Background()
	//fetch
	//fetch the repository forks with page =
	opts := &github.RepositoryListForksOptions{
		ListOptions: github.ListOptions{PerPage: 100, Page: page},
	}
	forks, resp, err := client.Repositories.ListForks(ctx, "netlify", "build-plugin-template", opts)
	if err != nil {
		return err
	}
	for _, fork := range forks {
		if fork.CloneURL != nil {
			repoMap.upsertFork(*fork.CloneURL)
		}
	}

	header := resp.Header.Get("Link")

	links := link.Parse(header)
	for _, link := range links {
		if link.Rel == "next" {
			url, err := url.Parse(link.URI)
			if err != nil {
				return err
			}
			page := url.Query().Get("page")

			i, err := strconv.Atoi(page)
			if err != nil {
				return err
			}
			resolveForks(client, "asdasd", i, repoMap)
		}
	}
	return nil
}

func cloneRepository(uri string, repoMap *RepoMap) {
	tempDir, err := os.MkdirTemp("", "repo-")
	if err != nil {
		// log.Fatal(err)
		log.Fatal(err)
	}
	defer os.RemoveAll(tempDir)
	localRepo, err := git.PlainClone(tempDir, false, &git.CloneOptions{
		URL:      uri,
		Progress: os.Stdout,
	})
	if err != nil {
		fmt.Println(err, uri)
	}

	// Map to store unique commit hashes

	// Get all references (local and remote branches)
	refs, err := localRepo.References()
	if err != nil {
		fmt.Println(err, uri)
	}

	err = refs.ForEach(func(ref *plumbing.Reference) error {
		if ref.Type() == plumbing.HashReference {
			commitIter, err := localRepo.Log(&git.LogOptions{From: ref.Hash()})
			if err != nil {
				return err
			}
			err = commitIter.ForEach(func(commit *object.Commit) error {
				repoMap.upsert(commit.Hash)
				repoMap.upsertShortSha(firstN(commit.Hash.String(), 4))
				return nil
			})
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		fmt.Println(err)
	}
}

func firstN(str string, n int) string {
	v := []rune(str)
	if n >= len(v) {
		return str
	}
	return string(v[:n])
}
