package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ChimeraCoder/anaconda"
	"github.com/dgraph-io/badger/v4"
	bopt "github.com/dgraph-io/badger/v4/options"
	"github.com/dgraph-io/badger/v4/y"
	"github.com/hypermodeinc/dgraph/x"
	"github.com/hypermodeinc/dgraph/xidmap"
)

const (
	cTimeFormat       = "Mon Jan 02 15:04:05 -0700 2006"
	cDgraphTimeFormat = "2006-01-02T15:04:05.999999999+10:00"
)

var (
	opts  progOptions
	alloc *xidmap.XidMap

	errNotATweet = errors.New("message in the stream is not a tweet")
)

type progOptions struct {
	NumWorkers        int
	InputPath         string
	OutputPath        string
	XidMapPath        string
	ZeroHost          string
	ReportEveryTweets int
}

type twitterUser struct {
	UID              uint64 `json:"uid,omitempty"`
	UserID           string `json:"user_id,omitempty"`
	UserName         string `json:"user_name,omitempty"`
	ScreenName       string `json:"screen_name,omitempty"`
	Description      string `json:"description,omitempty"`
	FriendsCount     int    `json:"friends_count,omitempty"`
	Verified         bool   `json:"verified,omitempty"`
	ProfileBannerURL string `json:"profile_banner_url,omitempty"`
	ProfileImageURL  string `json:"profile_image_url,omitempty"`
}

type twitterTweet struct {
	UID       uint64        `json:"uid,omitempty"`
	IDStr     string        `json:"id_str"`
	CreatedAt string        `json:"created_at"`
	Message   string        `json:"message,omitempty"`
	URLs      []string      `json:"urls,omitempty"`
	HashTags  []string      `json:"hashtags,omitempty"`
	Author    twitterUser   `json:"author"`
	Mention   []twitterUser `json:"mention,omitempty"`
	Retweet   bool          `json:"retweet"`
}

func main() {
	numWorkers := flag.Int("n", 4, "number of workers to run")
	inputPath := flag.String("i", "json", "path to input data")
	outputPath := flag.String("o", "pp", "path to output data")
	xidMapPath := flag.String("x", "xids", "path to store xid mapping")
	zeroHost := flag.String("z", "127.0.0.1:5080", "Dgraph zero gRPC server address")
	flag.Parse()

	opts = progOptions{
		NumWorkers:        *numWorkers,
		InputPath:         *inputPath,
		OutputPath:        *outputPath,
		XidMapPath:        *xidMapPath,
		ZeroHost:          *zeroHost,
		ReportEveryTweets: 1000,
	}

	// setup xidmap
	x.Check(os.MkdirAll(opts.XidMapPath, 0700))

	o := badger.DefaultOptions
	o.Dir = opts.XidMapPath
	o.ValueDir = opts.XidMapPath
	o.TableLoadingMode = bopt.MemoryMap
	o.SyncWrites = false
	db, err := badger.Open(o)
	x.Checkf(err, "Error while creating badger KV posting store")

	connzero, err := x.SetupConnection(opts.ZeroHost, nil, false)
	x.Checkf(err, "Unable to connect to zero, Is it running at %s?", opts.ZeroHost)

	alloc = xidmap.New(connzero, db)
	defer alloc.Flush()
	defer db.Close()

	// start workers
	c := y.NewCloser(opts.NumWorkers)
	work := make(chan string, 10000)
	for i := 0; i < opts.NumWorkers; i++ {
		go doWork(i, c, work, opts.OutputPath)
	}

	if err := filepath.Walk(opts.InputPath,
		func(path string, info os.FileInfo, err error) error {
			if err != nil {
				fmt.Println("error occurred while walking directory ::", err)
				return err
			}

			if !strings.HasSuffix(path, ".json") {
				return nil
			}

			return processFile(path, work)
		}); err != nil {
		panic(err)
	}

	close(work)
	c.Wait()
}

func processFile(path string, work chan<- string) error {
	fmt.Println("processing", path)

	fd, err := os.Open(path)
	if err != nil {
		fmt.Println("error in processing file ::", err)
		return err
	}
	defer fd.Close()

	scanner := bufio.NewScanner(fd)
	for scanner.Scan() {
		work <- scanner.Text()
	}

	if err := scanner.Err(); err != nil {
		fmt.Println("error in reading file ::", err)
		return err
	}

	return nil
}

func doWork(id int, c *y.Closer, work <-chan string, outDir string) {
	defer c.Done()

	fd, err := os.Create(fmt.Sprintf("%s/twitter_feed_pp_%d.json", outDir, id))
	if err != nil {
		panic(err)
	}
	defer fd.Close()

	writer := bufio.NewWriter(fd)
	if _, err := writer.WriteString("[\n"); err != nil {
		panic(err)
	}

	var totalMessages, erroredMessages, notTweetMessages int
	var noErr bool
	for message := range work {
		if noErr {
			if _, err := writer.WriteString(",\n"); err != nil {
				panic(err)
			}
		}
		totalMessages++

		if err := processMessage(writer, message); err != nil {
			noErr = false
			if err == errNotATweet {
				notTweetMessages++
			} else {
				erroredMessages++
			}

			continue
		}

		noErr = true
		if totalMessages%opts.ReportEveryTweets == 0 {
			fmt.Printf("Routine: %d, Total: %d, notTweet:%d, error: %d\n", id,
				totalMessages, notTweetMessages, erroredMessages)
		}
	}

	if _, err := writer.WriteString("\n]"); err != nil {
		panic(err)
	}
	writer.Flush()

	fmt.Printf("Routine: %d, Total: %d, notTweet:%d, error: %d\n", id,
		totalMessages, notTweetMessages, erroredMessages)
}

func processMessage(writer io.Writer, message string) error {
	tweet, err := parseMessage(message)
	if err != nil {
		return err
	}

	// produce RDF data from tweet
	if err := tweetToJSON(writer, tweet); err != nil {
		return err
	}

	return nil
}

func parseMessage(message string) (*anaconda.Tweet, error) {
	var tweet anaconda.Tweet
	if err := json.Unmarshal([]byte(message), &tweet); err != nil {
		return nil, err
	}

	if tweet.IdStr == "" {
		return nil, errNotATweet
	}

	return &tweet, nil
}

func tweetToJSON(writer io.Writer, tweet *anaconda.Tweet) error {
	createdAt, err := time.Parse(cTimeFormat, tweet.CreatedAt)
	if err != nil {
		return err
	}

	expandedURLs := make([]string, len(tweet.Entities.Urls))
	for _, url := range tweet.Entities.Urls {
		expandedURLs = append(expandedURLs, url.Expanded_url)
	}

	hashTagTexts := make([]string, 0)
	for _, tag := range tweet.Entities.Hashtags {
		if tag.Text != "" {
			hashTagTexts = append(hashTagTexts, tag.Text)
		}
	}

	var userMentions []twitterUser
	for _, userMention := range tweet.Entities.User_mentions {
		if userMention.Id_str == "" {
			return errNotATweet
		}

		userMentions = append(userMentions, twitterUser{
			UID:        alloc.AssignUid(userMention.Id_str),
			UserID:     userMention.Id_str,
			UserName:   userMention.Name,
			ScreenName: userMention.Screen_name,
		})
	}

	dt := twitterTweet{
		UID:       alloc.AssignUid(tweet.IdStr),
		IDStr:     tweet.IdStr,
		CreatedAt: createdAt.Format(cDgraphTimeFormat),
		Message:   unquote(strconv.Quote(tweet.FullText)),
		URLs:      expandedURLs,
		HashTags:  hashTagTexts,
		Author: twitterUser{
			UID:              alloc.AssignUid(tweet.User.IdStr),
			UserID:           tweet.User.IdStr,
			UserName:         unquote(strconv.Quote(tweet.User.Name)),
			ScreenName:       tweet.User.ScreenName,
			Description:      unquote(strconv.Quote(tweet.User.Description)),
			FriendsCount:     tweet.User.FriendsCount,
			Verified:         tweet.User.Verified,
			ProfileBannerURL: tweet.User.ProfileBannerURL,
			ProfileImageURL:  tweet.User.ProfileImageURL,
		},
		Mention: userMentions,
		Retweet: tweet.Retweeted,
	}

	data, err := json.Marshal(dt)
	if err != nil {
		return err
	}

	if _, err := writer.Write(data); err != nil {
		return err
	}

	return nil
}

func unquote(s string) string {
	return s[1 : len(s)-1]
}
