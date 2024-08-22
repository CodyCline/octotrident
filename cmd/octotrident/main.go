package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"

	githubRL "github.com/gofri/go-github-ratelimit/github_ratelimit"
	"github.com/google/go-github/v63/github"
	"github.com/joho/godotenv"
	"github.com/peterhellberg/link"
	"golang.org/x/exp/rand"
	"gopkg.in/src-d/go-git.v4"
	"gopkg.in/src-d/go-git.v4/plumbing"
	"gopkg.in/src-d/go-git.v4/plumbing/object"
)

type Repository struct {
	mutex             *sync.RWMutex
	KnownCommits      map[string]bool
	KnownShortCommits map[string]bool
	Forks             map[string]string
	HiddenCommits     []string
	//Non-existing commits that were attempted
	Duds         []string
	GuessCommits []string
}

type Runner struct {
	Clients []*github.Client
}

func NewRunner(apiKeys []string) (*Runner, error) {
	//Todo get keys
	if len(apiKeys) == 0 {
		return nil, errors.New("no api keys provided")
	}
	var clients []*github.Client
	for _, apiKey := range apiKeys {
		rateLimiter, err := githubRL.NewRateLimitWaiterClient(nil)
		if err != nil {
			return nil, err
		}
		clients = append(clients, github.NewClient(rateLimiter).WithAuthToken(apiKey))
	}

	return &Runner{
		Clients: clients,
	}, nil
}

func (r *Runner) RandomClient() *github.Client {
	return r.Clients[rand.Intn(len(r.Clients))]
}

type Commit struct {
	OID *string `json:"oid"`
}
type ObjectResponse struct {
	Data struct {
		Repository map[string]Commit `json:"repository"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
	Message string `json:"message"`
}

func handleRequests(client *github.Client, requests <-chan string, wg *sync.WaitGroup) {
	defer wg.Done()
	//Concurrency seems to be tricky returning many 502 if the concurrency is too high
	//Either leave the concurrency low and use more API keys or use something like oxylabs residential proxy
	//Unsure if it's a
	semaphore := make(chan string, 5) // Limit to 100 concurrent goroutines.

	for request := range requests {
		wg.Add(1)
		semaphore <- request // Acquire a token.
		go func(query string) {
			defer wg.Done()
			guessCommits(client, query)
			//Guess the commit here
			<-semaphore // Release a token.
		}(request)
	}
}

func main() {
	// Replace with the URL of the GitHub repository you want to clone
	repoURL := "https://github.com/netlify/build-plugin-template"
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}

	// Create a temporary directory
	commitMap := Repository{
		mutex:             &sync.RWMutex{},
		Forks:             make(map[string]string),
		KnownShortCommits: make(map[string]bool),
		KnownCommits:      make(map[string]bool),
		HiddenCommits:     []string{},
		GuessCommits:      []string{},
	}

	cloneRepository(repoURL, &commitMap)

	// Create a temporary directory
	// Clean up after the program ends
	keys := os.Getenv("GITHUB_KEYS")
	apiKeys := strings.Split(keys, ",")
	runner, err := NewRunner(apiKeys)
	if err != nil {
		panic(err)
	}

	fmt.Println(len(runner.Clients))

	initClient := runner.RandomClient()

	if err := resolveForks(initClient, repoURL, 1, &commitMap); err != nil {
		fmt.Println(err)
	}
	fmt.Println(len(commitMap.KnownCommits))
	var wg sync.WaitGroup
	// fmt.Println(len(commitMap.Forks))
	for _, fork := range commitMap.Forks {
		wg.Add(1)
		go func(fork string, cm *Repository) {
			defer wg.Done()
			cloneRepository(fork, cm)
		}(fork, &commitMap)
	}

	wg.Wait()
	generateShortSHAStrings(4, &commitMap)
	batches := createBatchQueries(commitMap.GuessCommits, 400)
	fmt.Println(len(batches), "batch queries to run")

	//Here we create throtteld queues to parallel fetch for each client token

	requestChannels := make([]chan string, len(runner.Clients))
	for i := range requestChannels {
		requestChannels[i] = make(chan string) // No need to buffer here.
	}

	for i, client := range runner.Clients {
		wg.Add(1)
		go handleRequests(client, requestChannels[i], &wg)
	}

	for i, batch := range batches {
		clientIndex := i % len(runner.Clients)
		requestChannels[clientIndex] <- batch // Send request to the appropriate client's channel.
	}

	for _, ch := range requestChannels {
		close(ch)
	}

	wg.Wait()

	fmt.Println(len(commitMap.HiddenCommits), len(commitMap.Duds))
	// Dump the response
}

func guessCommits(client *github.Client, query string) (*ObjectResponse, error) {
	req, err := client.NewRequest("POST", "/graphql", map[string]string{"query": query})
	if err != nil {
		return nil, err
	}

	var objectResponse *ObjectResponse
	g, err := client.Do(context.Background(), req, &objectResponse)
	fmt.Printf("%d \n", g.StatusCode)
	if err != nil {
		return nil, err
	}
	// for _, commit := range objectResponse.Data.Repository {
	// 	if commit.OID != nil {
	// 		fmt.Println(*commit.OID)
	// 	}
	// }
	return objectResponse, nil
}
func (rm *Repository) upsert(hash plumbing.Hash) {
	rm.mutex.Lock()
	defer rm.mutex.Unlock()
	commit := hash.String()
	if _, exists := (rm.KnownCommits)[commit]; !exists {
		//Add seen commits to hashmap
		(rm.KnownCommits)[commit] = true
	}

}

// Template out batched queries by grouping the short hash by batch size
// Next use string build to concat those fragment together in grouped queries and then return them as batch requests
func createBatchQueries(hashes []string, batchSize int) []string {
	batches := make([]string, 0, len(hashes)/batchSize+1)
	var fragmentBuilder strings.Builder
	for i, hash := range hashes {
		commitFragment := fmt.Sprintf(`cm%s: object(expression: "%s") {... on Commit {oid}}`, hash, hash)
		fragmentBuilder.WriteString(commitFragment)
		if (i+1)%batchSize == 0 || i == len(hashes)-1 {
			query := fmt.Sprintf(`query { repository(owner: "%s", name: "%s") {%s}}`, "netlify", "build-plugin-template", fragmentBuilder.String())
			batches = append(batches, query)
			fragmentBuilder.Reset()
		}
	}
	return batches
}

func (rm *Repository) upsertShortSha(hash string) {
	rm.mutex.Lock()
	defer rm.mutex.Unlock()
	if _, exists := (rm.KnownShortCommits)[hash]; !exists {
		(rm.KnownShortCommits)[hash] = true
	}
}

func (rm *Repository) upsertFork(url string) {
	rm.mutex.Lock()
	defer rm.mutex.Unlock()
	if _, exists := (rm.Forks)[url]; !exists {
		//Add seen commits to hashmap
		(rm.Forks)[url] = url
	}

}

func (rm *Repository) hasSHACollision(hash string) bool {
	rm.mutex.RLock()
	defer rm.mutex.RUnlock()
	if _, exists := (rm.KnownShortCommits)[hash]; exists {
		return true
	}
	return false

}

func resolveKnownCommits() {

}

func resolveForks(client *github.Client, uri string, page int, repoMap *Repository) error {
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

func cloneRepository(uri string, repoMap *Repository) error {
	tempDir, err := os.MkdirTemp("", "octotrident-repo-")
	defer os.RemoveAll(tempDir)

	if err != nil {
		return err
	}
	localRepo, err := git.PlainClone(tempDir, false, &git.CloneOptions{
		URL: uri,
	})
	if err != nil {
		return err
	}

	refs, err := localRepo.References()
	if err != nil {
		return err
	}

	err = refs.ForEach(func(ref *plumbing.Reference) error {
		if ref.Type() == plumbing.HashReference {
			commitIter, err := localRepo.Log(&git.LogOptions{From: ref.Hash()})
			if err != nil {
				return err
			}
			err = commitIter.ForEach(func(commit *object.Commit) error {
				repoMap.upsert(commit.Hash)
				shortHash := firstN(commit.Hash.String(), 4)
				repoMap.upsertShortSha(shortHash)
				return nil
			})
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

func firstN(str string, n int) string {
	v := []rune(str)
	if n >= len(v) {
		return str
	}
	return string(v[:n])
}

func generateShortSHAStrings(charLen int, repository *Repository) {
	hexDigits := "0123456789abcdef"
	var generateCombinations func(prefix string, length int)

	generateCombinations = func(prefix string, length int) {
		if length == 0 {
			if !repository.hasSHACollision(prefix) {
				repository.GuessCommits = append(repository.GuessCommits, prefix)
			}
			return
		}
		for _, digit := range hexDigits {
			generateCombinations(prefix+string(digit), length-1)
		}
	}
	generateCombinations("", charLen)
}
