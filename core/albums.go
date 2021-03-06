// Copyright 2016 Timothy Gion
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/user"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gomodule/oauth1/oauth"
)

const (
	apiRoot      = "https://api.smugmug.com"
	apiCurUser   = apiRoot + "/api/v2!authuser"
	apiAlbums    = "!albums"
	searchAlbums = apiRoot + "/api/v2/album!search"
	imagesAlbums = apiRoot + "/api/v2/album/%s!images"
	gNumWorkers  = 20
	gAlbumOnly   = ""
)

const albumPageSize = 100

var gAlbums []albumJSON
var gImages []imageJSON

type uriJSON struct {
	URI string `json:"Uri"`
}

type pagesJSON struct {
	Total          int
	Start          int
	Count          int
	RequestedCount int
	NextPage       string
}

type searchAlbumJSON struct {
	AlbumKey string
	Name     string
}

type urisJSON struct {
	LargestImage string `json:",omitempty"`
	LargestVideo string `json:",omitempty"`
}

type imageJSON struct {
	URI      string `json:"Uri"`
	FileName string
	Date     string
	IsVideo  bool
	ImageKey string
	Format   string
	Uris     urisJSON
}

type albumJSON struct {
	URI     string `json:"Uri"`
	URLName string `json:"UrlName"`
}

func (a albumJSON) key() string {
	tokens := strings.Split(a.URI, "/")
	return tokens[len(tokens)-1]
}

// Sort album array by UrlName for printing.
type byURLName []albumJSON

func (b byURLName) Len() int           { return len(b) }
func (b byURLName) Swap(i, j int)      { b[i], b[j] = b[j], b[i] }
func (b byURLName) Less(i, j int) bool { return b[i].URLName < b[j].URLName }

type endpointJSON struct {
	Album []albumJSON
	Pages pagesJSON
	User  uriJSON
}

type imagesRespJSON struct {
	AlbumImage []imageJSON
	Pages      pagesJSON
}

type albumImages struct {
	AlbumImage []imageJSON
	Dir        string
	Album      albumJSON
}

func (a albumImages) FixDoublon() bool {
	var m = make(map[string]bool)
	for ind, i := range a.AlbumImage {
		if m[i.FileName] {
			var extension = path.Ext(i.FileName)
			var name = i.FileName[0 : len(i.FileName)-len(extension)]
			a.AlbumImage[ind].FileName = name + "1" + extension
			return true
		}
		m[i.FileName] = true
	}
	return false
}

type downloadImage struct {
	AlbumImage imageJSON
	Dir        string
	Album      albumJSON
}

// Standard top level response from SmugMug API.
type responseJSON struct {
	Response endpointJSON
}

type imagesJSON struct {
	Response imagesRespJSON
}

type searchJSON struct {
	Album []searchAlbumJSON
	Pages pagesJSON
}

// Top level response for search from SmugMug API.
type searchResponseJSON struct {
	Response searchJSON
}

type largestVideo struct {
	MD5  string
	URL  string `json:"Url"`
	Size int64
}

type largestVideoJSON struct {
	LargestVideo largestVideo
}

type largestVideoResponseJSON struct {
	Response largestVideoJSON
}

type largestImage struct {
	MD5  string
	URL  string `json:"Url"`
	Size int64
}

type largestImageJSON struct {
	LargestImage largestImage
}

type largestImageResponseJSON struct {
	Response largestImageJSON
}

// getUser retrieves the URI that serves the current user.
func getUser(userToken *oauth.Credentials) (string, error) {
	var queryParams = url.Values{
		"_accept":    {"application/json"},
		"_verbosity": {"1"},
	}
	resp, err := oauthClient.Get(nil, userToken, apiCurUser, queryParams)
	if err != nil {
		log.Println("Error getting user endpoint: " + err.Error())
		return "", err
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Println("getUser response: " + resp.Status)
		return "", fmt.Errorf("getUser response: " + resp.Status)
	}

	bytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Println("Error reading user endpoint: " + err.Error())
		return "", err
	}

	var respJSON responseJSON
	err = json.Unmarshal(bytes, &respJSON)
	if err != nil {
		log.Println("Error decoding user endpoint JSON: " + err.Error())
		return "", err
	}

	if respJSON.Response.User.URI == "" {
		fmt.Println("No Uri object found in getUser response.")
		return "", errors.New("No Uri object found in getUser response")
	}

	return respJSON.Response.User.URI, nil
}

// printAlbums prints all the albums after sorting alphabetically.
func printAlbums(albums []albumJSON) {
	sort.Sort(byURLName(albums))
	for _, album := range albums {
		fmt.Println(album.URLName + " :: " + album.key())
	}
	gAlbums = albums
}

// aggregateTerms combines search terms into a single string with each search
// term separated by a plus sign.
func aggregateTerms(terms []string) string {
	var combinedTerms string
	for i, term := range terms {
		combinedTerms += term
		if i < len(terms)-1 {
			combinedTerms += "+"
		}
	}

	return combinedTerms
}

// search is the entry point to album search.
func search(terms []string) {
	userToken, err := loadUserToken()
	if err != nil {
		log.Println("Error reading OAuth token: " + err.Error())
		return
	}

	userURI, err := getUser(userToken)
	if err != nil {
		return
	}

	combinedTerms := aggregateTerms(terms)
	var client = http.Client{}

	searchRequest(&client, userToken, userURI, combinedTerms, 1)
}

// searchRequest sends the search request to SmugMug and asks for the entries beginning at start.
func searchRequest(client *http.Client, userToken *oauth.Credentials, userURI string, query string, start int) {
	var queryParams = url.Values{
		"_accept":       {"application/json"},
		"_verbosity":    {"1"},
		"_filter":       {"Album,Name,AlbumKey"},
		"_filteruri":    {""},
		"Scope":         {userURI},
		"SortDirection": {"Descending"},
		"SortMethod":    {"Rank"},
		"Text":          {query},
		"start":         {fmt.Sprintf("%d", start)},
		"count":         {"15"},
	}

	resp, err := oauthClient.Get(client, userToken, searchAlbums, queryParams)
	if err != nil {
		return
	}

	bytes, err := func() ([]byte, error) {
		defer resp.Body.Close()
		b, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		return b, nil
	}()

	if err != nil {
		log.Println("Reading search results: " + err.Error())
		return
	}

	var respJSON searchResponseJSON
	err = json.Unmarshal(bytes, &respJSON)
	if err != nil {
		log.Println("Decoding album search endpoint JSON: " + err.Error())
		return
	}

	if len(respJSON.Response.Album) < 1 {
		fmt.Println("No search results found.")
		return
	}

	printSearchResults(respJSON.Response.Album)

	pages := &respJSON.Response.Pages
	if pages.Count+pages.Start < pages.Total {
		fmt.Println("Press Enter for more results or Ctrl-C to quit.")
		var foo string
		fmt.Scanln(&foo)
		searchRequest(client, userToken, userURI, query, pages.Count+pages.Start)
	}
}

// printSearchResults outputs the album names and keys to stdout.
func printSearchResults(results []searchAlbumJSON) {
	for _, album := range results {
		fmt.Println(album.Name + " :: " + album.AlbumKey)
	}
}

func getImages(client *http.Client, userToken *oauth.Credentials,
	albumsURI string, start int, count int,
	epChan chan imagesRespJSON) {
	var queryParams = url.Values{
		"_accept":    {"application/json"},
		"_verbosity": {"1"},
		"start":      {fmt.Sprintf("%d", start)},
		"count":      {fmt.Sprintf("%d", count)},
	}

	resp, err := oauthClient.Get(client, userToken, albumsURI, queryParams)
	if err != nil {
		return
	}

	bytes, err := func() ([]byte, error) {
		defer resp.Body.Close()
		b, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		return b, nil
	}()

	if err != nil {
		log.Println("Reading images: " + err.Error())
		return
	}

	//fmt.Println(string(bytes))
	var respJSON imagesJSON
	err = json.Unmarshal(bytes, &respJSON)

	if err != nil {
		log.Println("Decoding images endpoint JSON: " + err.Error())
		return
	}

	if len(respJSON.Response.AlbumImage) < 1 {
		//fmt.Println("No images found.")
		//return
	}

	epChan <- respJSON.Response
}

func getHTTP(client *http.Client, userToken *oauth.Credentials, urla string) (bodyBytes []byte, err error) {

	var queryParams = url.Values{}

	var resp *http.Response
	resp, err = oauthClient.Get(client, userToken, urla, queryParams)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		fmt.Println("getHTTP response: " + resp.Status)
		return nil, fmt.Errorf("getHTTP response: " + resp.Status)
	}

	if bodyBytes, err = ioutil.ReadAll(resp.Body); err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return bodyBytes, nil
}

func downloadOneImage(client *http.Client, userToken *oauth.Credentials, j downloadImage) (err error) {
	var extension = path.Ext(j.AlbumImage.FileName)
	var name = j.AlbumImage.FileName[0 : len(j.AlbumImage.FileName)-len(extension)]
	target := path.Join(j.Dir, name+"_"+j.AlbumImage.ImageKey+extension)

	var bodyBytes []byte
	var downloadurl string
	var size int64

	if j.AlbumImage.IsVideo {
		uri := apiRoot + j.AlbumImage.Uris.LargestVideo

		var queryParams = url.Values{
			"_accept":    {"application/json"},
			"_verbosity": {"1"},
			"start":      {fmt.Sprintf("%d", 1)},
			"count":      {fmt.Sprintf("%d", 1)},
		}

		var resp *http.Response
		resp, err = oauthClient.Get(client, userToken, uri, queryParams)
		if err != nil {
			return err
		}

		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			fmt.Println("getUser response: " + resp.Status)
			return fmt.Errorf("getUser response: " + resp.Status)
		}

		bytes, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			log.Println("Error reading user endpoint: " + err.Error())
			return err
		}

		var respJSON largestVideoResponseJSON
		err = json.Unmarshal(bytes, &respJSON)
		if err != nil {
			log.Println("Error decoding largestVideoResponseJSON endpoint JSON: " + err.Error())
			return err
		}

		downloadurl = respJSON.Response.LargestVideo.URL
		size = respJSON.Response.LargestVideo.Size
	} else {
		uri := apiRoot + j.AlbumImage.Uris.LargestImage

		var queryParams = url.Values{
			"_accept":    {"application/json"},
			"_verbosity": {"1"},
			"start":      {fmt.Sprintf("%d", 1)},
			"count":      {fmt.Sprintf("%d", 1)},
		}

		var resp *http.Response
		resp, err = oauthClient.Get(client, userToken, uri, queryParams)
		if err != nil {
			return err
		}

		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			fmt.Println("getUser response: " + resp.Status)
			return fmt.Errorf("getUser response: " + resp.Status)
		}

		bytes, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			log.Println("Error reading user endpoint: " + err.Error())
			return err
		}

		var respJSON largestImageResponseJSON
		err = json.Unmarshal(bytes, &respJSON)
		if err != nil {
			log.Println("Error decoding largestImageResponseJSON endpoint JSON: " + err.Error())
			return err
		}

		downloadurl = respJSON.Response.LargestImage.URL
		size = respJSON.Response.LargestImage.Size
	}

	if stat, err := os.Stat(target); !os.IsNotExist(err) && !stat.IsDir() {
		if size == 0 || stat.Size() == size {
			//fmt.Printf("SKIP img %s album %s\n", j.AlbumImage.FileName, j.Album.URLName)
			return nil
		}
		fmt.Printf("WTF img %s album %s (%d != %d)\n", j.AlbumImage.FileName, j.Album.URLName, stat.Size(), size)
	}

	if bodyBytes, err = getHTTP(client, userToken, downloadurl); err != nil {
		return err
	}

	fmt.Printf("-> %s album %s\n", j.AlbumImage.FileName, j.Album.URLName)
	return ioutil.WriteFile(target, bodyBytes, 0644)
}

func workerFetchBuilds(client *http.Client, userToken *oauth.Credentials, id int, jobs <-chan downloadImage, results chan<- error) {
	for j := range jobs {
		if err := downloadOneImage(client, userToken, j); err != nil {
			fmt.Printf("Error img %s album %s: %v\n", j.AlbumImage.FileName, j.Album.URLName, err)
			results <- err
		} else {
			results <- nil
		}
	}
}

func lambdaRedirectPolicyFunc(req *http.Request, via []*http.Request) error {
	//req.Header.Add("x-api-key", GPrefs.Lambda.Token)
	//req.Header.Set("Content-Type", "application/json")
	return nil
}

func getAllImages() {
	userToken, err := loadUserToken()
	if err != nil {
		log.Println("Error reading OAuth token: " + err.Error())
		return
	}
	var client = &http.Client{CheckRedirect: lambdaRedirectPolicyFunc}

	allImgs := make([]albumImages, 0)
	usr, _ := user.Current()
	smugmugdir := path.Join(usr.HomeDir, "smugmug")
	for _, album := range gAlbums {
		if len(gAlbumOnly) != 0 && album.URLName != gAlbumOnly {
			continue
		}
		epChan := make(chan imagesRespJSON, 10)
		fmt.Printf("Requesting number of images for %s\n", album.URLName)
		imagesURI := fmt.Sprintf(imagesAlbums, album.key())
		getImages(client, userToken, imagesURI, 1, 1, epChan)
		ep := <-epChan

		fmt.Printf("%d images in %s\n", ep.Pages.Total, album.URLName)
		dir := path.Join(smugmugdir, album.URLName)
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			os.Mkdir(dir, 0777)
		}

		if ep.Pages.Count >= ep.Pages.Total {
			imgs := albumImages{Album: album, AlbumImage: ep.AlbumImage, Dir: dir}
			allImgs = append(allImgs, imgs)
			continue
		}

		waitGrp := sync.WaitGroup{}
		start := ep.Pages.Count + 1

		for start < ep.Pages.Total {
			//fmt.Printf("Requesting %d images starting at %d.\n", albumPageSize, start)
			waitGrp.Add(1)
			go func(startInd int) {
				defer waitGrp.Done()
				getImages(client, userToken, imagesURI, startInd, albumPageSize, epChan)
			}(start)
			start += albumPageSize
		}

		albumImgs := make([]imageJSON, 0, ep.Pages.Total)
		albumImgs = append(albumImgs, ep.AlbumImage...)

		albumsReqDoneChan := make(chan bool)
		resultsPrintedChan := make(chan bool)
		go collectImageResults(albumImgs, albumsReqDoneChan, epChan,
			resultsPrintedChan)

		waitGrp.Wait()

		// Tell collectImageResults() that all album requests finished.
		albumsReqDoneChan <- true

		// Wait for albums to be displayed.
		<-resultsPrintedChan

		imgs := albumImages{Album: album, AlbumImage: gImages, Dir: dir}
		allImgs = append(allImgs, imgs)
	}

	if false {
		for _, a := range allImgs {
			for {
				if !a.FixDoublon() {
					break
				}
			}
		}
	}

	file, _ := json.MarshalIndent(allImgs, "", " ")
	_ = ioutil.WriteFile(path.Join(smugmugdir, "allImgs.json"), file, 0644)

	var allDownloads = make([]downloadImage, 0)
	for _, a := range allImgs {
		for _, i := range a.AlbumImage {
			i := downloadImage{Album: a.Album, AlbumImage: i, Dir: a.Dir}
			allDownloads = append(allDownloads, i)
		}
	}

	var jobs = make(chan downloadImage, len(allDownloads))
	var results = make(chan error, len(allDownloads))

	for w := 1; w <= gNumWorkers; w++ { //runtime.NumCPU())
		go workerFetchBuilds(client, userToken, w, jobs, results)
	}

	for _, a := range allDownloads {
		jobs <- a
	}
	close(jobs)

	for range allDownloads {
		<-results
	}
}

// collectImageResults receives albums over epChan from getAlbumPage().  It
// continues to listen to epChan until receiving true from albumsReqDoneChan.
// Finally, it outputs the albums to stdout and indicates completion by
// sending true over resultsPrintedChan.
func collectImageResults(
	albumImgs []imageJSON,
	albumsReqDoneChan chan bool,
	epChan chan imagesRespJSON,
	resultsPrintedChan chan bool) {

	done := false
	for !done || len(epChan) > 0 {
		select {
		case epAlbs := <-epChan:
			albumImgs = append(albumImgs, epAlbs.AlbumImage...)
		case done = <-albumsReqDoneChan:
		}
	}

	//printAlbums(albums)
	gImages = albumImgs
	resultsPrintedChan <- true
}

// albums lists all the albums (and their keys) that belong to the user.
func albums() {
	userToken, err := loadUserToken()
	if err != nil {
		log.Println("Error reading OAuth token: " + err.Error())
		return
	}

	userURI, err := getUser(userToken)
	if err != nil {
		return
	}

	startT := time.Now()
	albumsURI := apiRoot + userURI + apiAlbums
	var client = http.Client{}
	epChan := make(chan endpointJSON, 10)
	fmt.Println("Requesting number of albums.")
	getAlbumPage(&client, userToken, albumsURI, 1, 1, epChan)
	ep := <-epChan

	if ep.Pages.Count >= ep.Pages.Total {
		printAlbums(ep.Album)
		return
	}

	waitGrp := sync.WaitGroup{}
	start := ep.Pages.Count + 1

	for start < ep.Pages.Total {
		fmt.Printf("Requesting %d albums starting at %d.\n", albumPageSize, start)
		waitGrp.Add(1)
		go func(startInd int) {
			defer waitGrp.Done()
			getAlbumPage(&client, userToken, albumsURI, startInd, albumPageSize, epChan)
		}(start)
		start += albumPageSize
	}

	albums := make([]albumJSON, 0, ep.Pages.Total)
	copy(albums, ep.Album)

	albumsReqDoneChan := make(chan bool)
	resultsPrintedChan := make(chan bool)
	go collectAlbumResults(albums, albumsReqDoneChan, epChan,
		resultsPrintedChan)

	waitGrp.Wait()

	// Tell collectAlbumResults() that all album requests finished.
	albumsReqDoneChan <- true

	// Wait for albums to be displayed.
	<-resultsPrintedChan
	totalT := time.Since(startT)
	fmt.Println("\nElapsed time: " + totalT.String())
}

// collectAlbumResults receives albums over epChan from getAlbumPage().  It
// continues to listen to epChan until receiving true from albumsReqDoneChan.
// Finally, it outputs the albums to stdout and indicates completion by
// sending true over resultsPrintedChan.
func collectAlbumResults(
	albums []albumJSON,
	albumsReqDoneChan chan bool,
	epChan chan endpointJSON,
	resultsPrintedChan chan bool) {

	done := false
	for !done || len(epChan) > 0 {
		select {
		case epAlbs := <-epChan:
			albums = append(albums, epAlbs.Album...)
		case done = <-albumsReqDoneChan:
		}
	}

	printAlbums(albums)
	resultsPrintedChan <- true
}

// getAlbumPage gets up to count albums starting at index start.  It returns
// the album and page data over epChan, so it may be invoked as a goroutine.
func getAlbumPage(
	client *http.Client, userToken *oauth.Credentials,
	albumsURI string, start int, count int,
	epChan chan endpointJSON) {

	var queryParams = url.Values{
		"_accept":    {"application/json"},
		"_verbosity": {"1"},
		"start":      {fmt.Sprintf("%d", start)},
		"count":      {fmt.Sprintf("%d", count)},
	}

	resp, err := oauthClient.Get(client, userToken, albumsURI, queryParams)
	if err != nil {
		return
	}

	bytes, err := func() ([]byte, error) {
		defer resp.Body.Close()
		b, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		return b, nil
	}()

	if err != nil {
		log.Println("Reading albums: " + err.Error())
		return
	}

	var respJSON responseJSON
	err = json.Unmarshal(bytes, &respJSON)
	if err != nil {
		log.Println("Decoding album endpoint JSON: " + err.Error())
		return
	}

	if len(respJSON.Response.Album) < 1 {
		fmt.Println("No albums found.")
		return
	}

	epChan <- respJSON.Response
}

// createAlbum was test code for exercising the SmugMug API.  It works, but is
// hard coded for a particular album in a particular location.
func createAlbum(client *http.Client, credentials *oauth.Credentials) {
	createURI := apiRoot + "/api/v2/node/R3gfM!children"

	var body = map[string]string{
		"Type":    "Album",
		"Name":    "Test Post Create",
		"UrlName": "Test-Post-Create",
		"Privacy": "Public",
	}

	rawJSON, err := json.Marshal(body)
	if err != nil {
		return
	}
	fmt.Println(string(rawJSON))

	req, err := http.NewRequest("POST", createURI, bytes.NewReader(rawJSON))
	if err != nil {
		return
	}

	req.Header["Content-Type"] = []string{"application/json"}
	req.Header["Content-Length"] = []string{fmt.Sprintf("%d", len(rawJSON))}
	req.Header["Accept"] = []string{"application/json"}

	if err := oauthClient.SetAuthorizationHeader(
		req.Header, credentials, "POST", req.URL, url.Values{}); err != nil {
		// req.Header, credentials, "POST", req.URL, headers); err != nil {
		return
	}

	fmt.Println(req)

	var resp *http.Response
	resp, err = client.Do(req)
	if err != nil {
		return
	}

	defer resp.Body.Close()

	bytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return
	}

	fmt.Println(resp.Status)
	fmt.Println(string(bytes))
}
