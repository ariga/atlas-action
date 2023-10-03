// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package atlasaction

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

type GithubIssueComment struct {
	Id   int    `json:"id"`
	Body string `json:"body"`
}

type GithubAPI struct {
	baseURL string
}

func (g *GithubAPI) GetIssueComments(id int, repo, authToken string) ([]GithubIssueComment, error) {
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
	all, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	var comments []GithubIssueComment
	err = json.Unmarshal(all, &comments)
	if err != nil {
		return nil, fmt.Errorf("error parsing github comments with %v/%v from %v, %w", repo, id, string(all), err)
	}
	return comments, nil
}

func (g *GithubAPI) CreateIssueComment(id int, content io.Reader, repo, authToken string) error {
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

func (g *GithubAPI) UpdateComment(id int, content io.Reader, repo, authToken string) error {
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
