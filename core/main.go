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
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"strconv"
	"strings"

	"github.com/gomodule/oauth1/oauth"
)

// The names of the token files.
const (
	apiTokenFile  = "apiToken.json"
	userTokenFile = "userToken.json"
)

var retriesFlag uint

// loadToken imports tokens from the given JSON file.
func loadToken(filename string) (*oauth.Credentials, error) {
	bytes, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	var token oauth.Credentials
	if err := json.Unmarshal(bytes, &token); err != nil {
		return nil, err
	}
	return &token, nil
}

// usage gives minimal usage instructions.
func usage() {
	fmt.Println("Usage: ")
	fmt.Println(os.Args[0] + " [-retries n] apikey|auth|albums|search|upload|multiupload")
	fmt.Println("\tapikey")
	fmt.Println("\tauth")
	fmt.Println("\talbums")
	fmt.Println("\tsearch <search term 1> ... <search term n>")
	fmt.Println("\tupload <album key> <filename>")
	fmt.Println("\tmultiupload <# parallel uploads> <album key> <filename 1> ... <filename n>")
	fmt.Println("\nNumber of retries defaults to 2 if not specified.")
}

func init() {
	flag.UintVar(&retriesFlag, "retries", 2, "number of retries if upload fails")
}

func main() {
	flag.Parse()
	if len(flag.Args()) < 1 {
		usage()
		return
	}

	loweredCmd := strings.ToLower(flag.Arg(0))
	if loweredCmd == "apikey" {
		apikey()
		return
	}

	// Normal code path where an API key must exist.
	authInit()

	switch loweredCmd {
	case "auth":
		auth()
	case "upload":
		if len(flag.Args()) != 3 {
			usage()
			return
		}
		upload(flag.Arg(1), flag.Arg(2))
	case "albums":
		albums()
		getAllImages()
	case "search":
		if len(flag.Args()) < 2 {
			usage()
			return
		}
		search(flag.Args()[1:])
	case "multiupload":
		if len(flag.Args()) < 4 {
			usage()
			return
		}
		numParallel, err := strconv.Atoi(flag.Arg(1))
		if err != nil {
			usage()
			return
		}
		multiUpload(numParallel, flag.Arg(2), flag.Args()[3:])
	default:
		usage()
		return
	}
}
