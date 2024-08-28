package github_brute

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/go-github/v63/github"
)

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

func CreateQueries(hashes []string, batchSize int, owner string, repo string) []string {
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

func Brute(client *github.Client, query string) (*ObjectResponse, error) {
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
