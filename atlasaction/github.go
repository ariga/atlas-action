package atlasaction

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

type GithubIssueComment struct {
	Id                    int    `json:"id"`
	Body                  string `json:"body"`
	PerformedViaGithubApp any    `json:"performed_via_github_app"`
}

type Github struct {
	baseURL string
}

func NewGithub() *Github {
	return &Github{
		baseURL: "https://api.github.com",
	}
}

func (g *Github) GetIssueComments(id int, repo, authToken string) ([]GithubIssueComment, error) {
	url := fmt.Sprintf("%v/repos/ariga/%v/issues/%v/comments", g.baseURL, repo, id)
	client := &http.Client{}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	addHeaders(req, authToken)
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error querying github comments with %v/%v, %w", repo, id, err)
	}
	defer res.Body.Close()
	var comments []GithubIssueComment
	err = json.NewDecoder(res.Body).Decode(&comments)
	if err != nil {
		return nil, fmt.Errorf("error parsing github comments with %v/%v, %w", repo, id, err)
	}
	return comments, nil
}

func (g *Github) CreateIssueComment(id int, content io.Reader, repo, authToken string) error {
	url := fmt.Sprintf("%v/repos/ariga/%v/issues/%v/comments", g.baseURL, repo, id)
	client := &http.Client{}
	req, err := http.NewRequest(http.MethodPost, url, content)
	if err != nil {
		return err
	}
	addHeaders(req, authToken)
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 201 {
		b, err := io.ReadAll(res.Body)
		if err == nil {
			err = errors.New(string(b))
		}
		return fmt.Errorf("create github comment failed with %v: %w", res.StatusCode, err)
	}
	return err
}

func (g *Github) UpdateComment(id int, content io.Reader, repo, authToken string) error {
	url := fmt.Sprintf("%v/repos/ariga/%v/issues/comments/%v", g.baseURL, repo, id)
	client := &http.Client{}
	req, err := http.NewRequest(http.MethodPatch, url, content)
	if err != nil {
		return err
	}
	addHeaders(req, authToken)
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		b, err := io.ReadAll(res.Body)
		if err == nil {
			err = errors.New(string(b))
		}
		return fmt.Errorf("update github comment failed with %v: %w", res.StatusCode, err)
	}
	return err
}

func addHeaders(req *http.Request, authToken string) {
	req.Header.Add("Accept", "application/vnd.github+json")
	req.Header.Add("Authorization", "Bearer "+authToken)
	req.Header.Add("X-GitHub-Api-Version", "2022-11-28")
}
