package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"

	"github.com/ChimeraCoder/anaconda"
	"github.com/dgraph-io/badger/v4/y"
)

var (
	opts progOptions
)

type twitterCreds struct {
	AccessSecret   string `json:"access_secret"`
	AccessToken    string `json:"access_token"`
	ConsumerKey    string `json:"consumer_key"`
	ConsumerSecret string `json:"consumer_secret"`
}

type progOptions struct {
	NumWorkers        int
	CredentialsFile   string
	OutputPath        string
	ReportEveryTweets int
}

func main() {
	numWorkers := flag.Int("n", 4, "number of workers to run in parallel")
	credentialsFile := flag.String("c", "credentials.json", "path to credentials file")
	outputPath := flag.String("d", "", "folder to store the json tweets")
	flag.Parse()

	opts = progOptions{
		NumWorkers:        *numWorkers,
		CredentialsFile:   *credentialsFile,
		OutputPath:        *outputPath,
		ReportEveryTweets: 1000,
	}

	// setup twitter client
	creds, err := readCredentials(opts.CredentialsFile)
	if err != nil {
		panic(err)
	}

	client, err := newTwitterClient(creds)
	if err != nil {
		panic(err)
	}

	stream := client.PublicStreamSample(nil)
	defer stream.Stop()

	// read twitter stream
	c := y.NewCloser(0)
	for i := 0; i < opts.NumWorkers; i++ {
		c.AddRunning(1)
		go doWork(i, c, stream.C, opts.OutputPath)
	}

	c.Wait()
	log.Println("Stopping stream...")
}

func doWork(id int, c *y.Closer, work <-chan interface{}, outDir string) {
	defer c.Done()

	fd, err := os.Create(fmt.Sprintf("%s/twitter_feed_%d.json", outDir, id))
	if err != nil {
		panic(err)
	}
	defer fd.Close()

	writer := bufio.NewWriter(fd)
	var totalMessages, erroredMessages int
	for message := range work {
		totalMessages++

		data, err := json.Marshal(message)
		if err != nil {
			fmt.Println("error in marshalling feed item ::", err)
			erroredMessages++
			continue
		}

		data = append(data, '\n')
		if _, err := writer.Write(data); err != nil {
			fmt.Println("error writing to file ::", err)
			erroredMessages++
			continue
		}

		if totalMessages%opts.ReportEveryTweets == 0 {
			fmt.Printf("Routine: %d, Total: %d, error: %d\n", id,
				totalMessages, erroredMessages)
		}
	}
}

func readCredentials(path string) (*twitterCreds, error) {
	jsn, err := ioutil.ReadFile(path)
	if err != nil {
		log.Printf("Unable to open twitter credentials file '%s' :: %v", path, err)
		return nil, err
	}

	var creds twitterCreds
	err = json.Unmarshal(jsn, &creds)
	if err != nil {
		log.Printf("Unable to parse twitter credentials file '%s' :: %v", path, err)
		return nil, err
	}

	return &creds, nil
}

func newTwitterClient(creds *twitterCreds) (*anaconda.TwitterApi, error) {
	client := anaconda.NewTwitterApiWithCredentials(
		creds.AccessToken, creds.AccessSecret,
		creds.ConsumerKey, creds.ConsumerSecret,
	)

	if ok, err := client.VerifyCredentials(); err != nil {
		log.Printf("error in verifying credentials :: %v", err)
		return nil, err
	} else if !ok {
		log.Printf("invalid twitter credentials")
		return nil, errors.New("invalid credentials")
	}

	return client, nil
}
