package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	git "github.com/go-git/go-git/v5"
	config "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	githubRL "github.com/gofri/go-github-ratelimit/github_ratelimit"
	"github.com/google/go-github/v63/github"
	"github.com/joho/godotenv"
	giturl "github.com/kubescape/go-git-url"
	"github.com/peterhellberg/link"
	"golang.org/x/exp/rand"
)

type Repository struct {
	mutex             *sync.RWMutex
	KnownCommits      map[string]bool
	KnownShortCommits map[string]bool
	Forks             map[string]string
	HiddenCommits     []string
	//Non-existing commits that were attempted
	InvalidCommits []string
	GuessCommits   []string
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
		DefaultTransport := &http.Transport{
			// Proxy:                 http.ProxyURL(url),
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		}
		print := func(context *githubRL.CallbackContext) {
			fmt.Printf("Secondary rate limit reached! ")
			// time.Until(*context.SleepUntil).Seconds(), time.Now(), *context.SleepUntil)
		}

		rateLimiter, err := githubRL.NewRateLimitWaiterClient(DefaultTransport, githubRL.WithLimitDetectedCallback(print))
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

func handleRequests(client *github.Client, requests <-chan string, wg *sync.WaitGroup, repository *Repository) {
	defer wg.Done()
	//Concurrency seems to be tricky returning many 502 if the concurrency is too high
	//Either leave the concurrency low and use more API keys or use something like //oxylabs seems to make zero difference so it's an IP based thing ??
	//Unsure if it's a
	semaphore := make(chan string, 5) // Limit to 100 concurrent goroutines.

	for request := range requests {
		wg.Add(1)
		semaphore <- request // Acquire a token.
		go func(query string) error {
			defer wg.Done()

			for i := 3; i >= 0; i-- {
				fmt.Printf("retry count %d\n", i)
				resp, err := guessCommits(client, query)
				if err != nil {
					fmt.Println(err)
				}
				if resp != nil {
					repository.mutex.Lock()
					defer repository.mutex.Unlock()
					for k, commit := range resp.Data.Repository {
						if commit.OID != nil {
							repository.HiddenCommits = append(repository.HiddenCommits, *commit.OID)
						} else {
							repository.InvalidCommits = append(repository.InvalidCommits, k)
						}
					}
					break
				}
			}

			//Guess the commit here
			<-semaphore // Release a token.
			return nil
		}(request)
	}
}

func main() {
	// Replace with the URL of the GitHub repository you want to clone
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}
	keys := os.Getenv("GITHUB_KEYS")
	apiKeys := strings.Split(keys, ",")
	runner, err := NewRunner(apiKeys)
	if err != nil {
		panic(err)
	}

	initClient := runner.RandomClient()

	repoURL := "https://github.com/netlify/next-runtime"
	gitURL, err := giturl.NewGitURL(repoURL) // initialize and parse the URL
	if err != nil {
		log.Fatal(err)
	}

	owner, repositoryName := gitURL.GetOwnerName(), gitURL.GetRepoName()

	// Create a temporary directory
	commitMap := Repository{
		mutex:             &sync.RWMutex{},
		Forks:             make(map[string]string),
		KnownShortCommits: make(map[string]bool),
		KnownCommits:      make(map[string]bool),
		HiddenCommits:     []string{},
		GuessCommits:      []string{},
	}

	placeIt := path.Join(".", repositoryName)

	repository, _, err := cloneRepository(repoURL, &commitMap, placeIt)
	if err != nil {
		fmt.Println(err)
	}

	if err := fetchForksRecursively(initClient, owner, repositoryName, 1, &commitMap); err != nil {
		fmt.Println(err)
	}
	fmt.Println("commits", len(commitMap.KnownCommits))
	var wg sync.WaitGroup
	for _, fork := range commitMap.Forks {
		wg.Add(1)
		go func(forkURL string) error {
			defer wg.Done()
			commits, err := resolveFork(forkURL, &commitMap)
			if err != nil {
				return err
			}
			fmt.Println("New commits found", len(commits))
			err = fetchCommits(repository, repoURL, commits)
			if err != nil {
				return err
			}
			return nil
		}(fork)
	}

	wg.Wait()
	generateShortSHAStrings(4, &commitMap)
	batches := createBatchQueries(commitMap.GuessCommits, 300, owner, repositoryName)
	fmt.Println(len(batches), "batch queries to run")

	//Here we create throttled queues to parallel fetch for each client token

	requestChannels := make([]chan string, len(runner.Clients))
	for i := range requestChannels {
		requestChannels[i] = make(chan string)
	}

	for i, client := range runner.Clients {
		wg.Add(1)
		go handleRequests(client, requestChannels[i], &wg, &commitMap)
	}

	for i, batch := range batches {
		clientIndex := i % len(runner.Clients)
		requestChannels[clientIndex] <- batch // Send request to the appropriate client's channel.
	}

	for _, ch := range requestChannels {
		close(ch)
	}

	wg.Wait()
	fmt.Printf("FOUND: %d HIDDEN COMMITS\n", len(commitMap.HiddenCommits))
	batchCommits := createBatches(commitMap.HiddenCommits, 1000)
	for batch := range batchCommits {
		fetchCommits(repository, repoURL, batch)
	}
	//Check them out afterwards

	fmt.Printf("FINISHED processing repo found %d hidden commits use trufflehog on", len(commitMap.HiddenCommits))
}

func guessCommits(client *github.Client, query string) (*ObjectResponse, error) {
	req, err := client.NewRequest("POST", "/graphql", map[string]string{"query": query})
	if err != nil {
		return nil, err
	}

	var objectResponse *ObjectResponse
	_, err = client.Do(context.Background(), req, &objectResponse)
	if err != nil {
		return nil, err
	}
	return objectResponse, nil
}
func (rm *Repository) upsert(hash plumbing.Hash) bool {
	rm.mutex.Lock()
	defer rm.mutex.Unlock()
	commit := hash.String()
	if _, exists := (rm.KnownCommits)[commit]; !exists {
		//Add seen commits to hashmap
		(rm.KnownCommits)[commit] = true
		return false
	}
	return true
}

// Template out batched queries by grouping the short hash by batch size
// Next use string build to concat those fragment together in grouped queries and then return them as batch requests
func createBatchQueries(hashes []string, batchSize int, owner string, repo string) []string {
	batches := make([]string, 0, len(hashes)/batchSize+1)
	var fragmentBuilder strings.Builder
	for i, hash := range hashes {
		commitFragment := fmt.Sprintf(`cm%s: object(expression: "%s") {... on Commit {oid}}`, hash, hash)
		fragmentBuilder.WriteString(commitFragment)
		if (i+1)%batchSize == 0 || i == len(hashes)-1 {
			query := fmt.Sprintf(`query { repository(owner: "%s", name: "%s") {%s}}`, owner, repo, fragmentBuilder.String())
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

func createBatches(items []string, batchSize int) <-chan []string {
	out := make(chan []string)
	go func() {
		defer close(out)
		itemsCopy := append([]string(nil), items...)
		for len(itemsCopy) > 0 {
			end := batchSize
			if len(itemsCopy) < batchSize {
				end = len(itemsCopy)
			}
			batch := itemsCopy[:end]
			itemsCopy = itemsCopy[end:]
			out <- batch
		}
	}()
	return out
}

func runGitCommand(args []string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	out, err := cmd.CombinedOutput()
	return out, err
}

func fetchCommits(repository *git.Repository, forkURL string, commits []string) error {
	remoteName := "origin"
	remote, err := repository.Remote("origin")
	if err != nil {
		return err
	}
	var hashes []config.RefSpec

	for _, commitHash := range commits {
		remoteRef := plumbing.NewHashReference(plumbing.NewRemoteReferenceName(remoteName, commitHash), plumbing.NewHash(commitHash))
		refSpec := config.RefSpec(fmt.Sprintf("%v:%v", remoteRef.Hash().String(), remoteRef.Name().String()))
		hashes = append(hashes, refSpec)
	}

	err = remote.Fetch(&git.FetchOptions{
		RemoteName: remoteName,
		RemoteURL:  forkURL,
		RefSpecs:   hashes,
		Progress:   os.Stdout,
	})
	if err != nil {
		return err
	}
	return nil
}

func checkoutCommits(workTree *git.Worktree, repository *git.Repository, commits []string) error {
	fmt.Println(commits)
	for _, hash := range commits {
		// Create a new branch reference
		commitHash := plumbing.NewHash(hash)
		commit, err := repository.CommitObject(commitHash)
		if err != nil {
			fmt.Println("HEREZ", err)
			continue
		}

		refName := plumbing.NewBranchReferenceName(fmt.Sprintf("_%s", hash))
		newBranchRef := plumbing.NewHashReference(refName, commit.Hash)

		// Store the new branch reference
		err = repository.Storer.SetReference(newBranchRef)
		if err != nil {
			fmt.Println("HERES", err)
		}

		workTree.Checkout(&git.CheckoutOptions{
			Branch: refName,
			Force:  true,
		})
	}
	return nil
}

func fetchForksRecursively(client *github.Client, owner string, repo string, page int, repoMap *Repository) error {
	ctx := context.Background()
	//fetch the repository forks with page =
	opts := &github.RepositoryListForksOptions{
		ListOptions: github.ListOptions{PerPage: 100, Page: page},
	}
	forks, resp, err := client.Repositories.ListForks(ctx, owner, repo, opts)
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
			fetchForksRecursively(client, owner, repo, i, repoMap)
		}
	}
	return nil
}

func resolveFork(uri string, repoMap *Repository) ([]string, error) {
	var uniqueCommits []string
	location, err := os.MkdirTemp("", "*-octotrident-repo")
	defer os.RemoveAll(location)
	if err != nil {
		return nil, err
	}
	localRepo, err := git.PlainClone(location, false, &git.CloneOptions{
		URL: uri,
	})
	if err != nil {
		return nil, err
	}

	refs, err := localRepo.References()
	if err != nil {
		return nil, err
	}

	err = refs.ForEach(func(ref *plumbing.Reference) error {
		if ref.Type() == plumbing.HashReference {
			commitIter, err := localRepo.Log(&git.LogOptions{From: ref.Hash()})
			if err != nil {
				return err
			}
			err = commitIter.ForEach(func(commit *object.Commit) error {
				seen := repoMap.upsert(commit.Hash)
				shortHash := firstN(commit.Hash.String(), 4)
				repoMap.upsertShortSha(shortHash)
				if !seen {
					uniqueCommits = append(uniqueCommits, commit.Hash.String())
				}
				return nil
			})
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return uniqueCommits, nil
}

// Checkout each commit
func cloneRepository(uri string, repoMap *Repository, location string) (*git.Repository, *git.Worktree, error) {
	var workingRepo *git.Repository
	repo, err := git.PlainClone(location, false, &git.CloneOptions{
		URL:      uri,
		Progress: os.Stdout,
	})
	if err != nil {
		if err.Error() == git.ErrRepositoryAlreadyExists.Error() {
			localRepo, err := git.PlainOpen(location)
			if err != nil {
				fmt.Println("here")
				return nil, nil, err
			}
			workingRepo = localRepo
		} else {
			return nil, nil, err
		}
	} else {
		workingRepo = repo
	}
	tree, err := workingRepo.Worktree()
	if err != nil {
		fmt.Println(err)
	}

	refs, err := workingRepo.References()
	if err != nil {
		return nil, nil, err
	}

	refs.ForEach(func(ref *plumbing.Reference) error {
		if ref.Type() == plumbing.HashReference {
			commitIter, err := workingRepo.Log(&git.LogOptions{From: ref.Hash()})
			if err != nil {
				return err
			}
			commitIter.ForEach(func(commit *object.Commit) error {
				repoMap.upsert(commit.Hash)
				shortHash := firstN(commit.Hash.String(), 4)
				repoMap.upsertShortSha(shortHash)
				return nil
			})
			return nil
		}
		return nil
	})

	return workingRepo, tree, nil
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
