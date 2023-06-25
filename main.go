/*
*rss-to-tts A program to read rss articles to tts mp3s
*Copyright © 2022 Kevin Jiang
*
*Permission is hereby granted, free of charge, to any person obtaining
*a copy of this software and associated documentation files (the "Software"),
*to deal in the Software without restriction, including without limitation
*the rights to use, copy, modify, merge, publish, distribute, sublicense,
*and/or sell copies of the Software, and to permit persons to whom the
*Software is furnished to do so, subject to the following conditions:
*
*The above copyright notice and this permission notice shall be included
*in all copies or substantial portions of the Software.
*
*THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
*EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES
*OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT.
*IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM,
*DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT,
*TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE
*OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
 */

package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	texttospeech "cloud.google.com/go/texttospeech/apiv1"
	"github.com/KevinSJ/rss-to-podcast/internal/config"
	"github.com/KevinSJ/rss-to-podcast/internal/helper"
	"github.com/mmcdole/gofeed"
	"golang.org/x/exp/slices"
	"golang.org/x/sync/errgroup"
)

type WorkerRequest struct {
	// Item for this request
	Item *gofeed.Item

	// Directory to which the file wil write to
	Directory string

	// Language of the item
	LanguageCode string

	// Whether to use natural Voice
	UseNaturalVoice bool
}

func main() {
	configPath, _ := filepath.Abs("./config.yaml")
	config, err := config.NewConfig(configPath)
	if err != nil {
		log.Fatalf("Unable to parse config file, error: %v", err)
	}

	os.Setenv("GOOGLE_APPLICATION_CREDENTIALS", config.CredentialPath)

	fp := gofeed.NewParser()
	g := new(errgroup.Group)
	ctx := context.Background()

	client, err := texttospeech.NewClient(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	work := make(chan *WorkerRequest, config.MaxItemPerFeed*len(config.Feeds))

	var wg sync.WaitGroup
	for i := 0; i < config.ConcurrentWorkers; i++ {
		wg.Add(1)
		go speechSynthesizeWorker(&wg, client, &work, ctx)
	}

	for _, _v := range config.Feeds {
		v := _v
		g.Go(func() error {
			log.Printf("feed: %v\n", v)
			feed, err := fp.ParseURL(v)
			if err != nil {
				log.Fatalf("Error GET: %v\n", err)
			}

			hasValidItems := slices.IndexFunc(feed.Items, func(item *gofeed.Item) bool {
				return time.Since(item.PublishedParsed.Local()).Hours() <= config.ItemSince
			})

			if hasValidItems == -1 {
				return nil
			}

			// create folder based on RSS update date, this will be used to store all
			// generated mp3s.
			dir, err := helper.CreateDirectory(*feed)
			if err != nil {
				log.Panicf("error: %v", err)
			}

			createSpeechFromItems(feed, config, &work, dir)
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		log.Fatal(err.Error())
	}

	close(work)
	wg.Wait()

	log.Printf("Done processing all feeds")
}

func createSpeechFromItems(feed *gofeed.Feed, config *config.Config, work *chan *WorkerRequest, direcory *string) {
	log.Printf("feed.Title: %v\n", feed.Title)

	itemSize := func(size int, limit int) int {
		if size > limit {
			return limit
		}

		return size
	}(len(feed.Items), config.MaxItemPerFeed)

	isInRange := func(itemPublishTime *time.Time) bool {
		return time.Since((*itemPublishTime).Local()).Hours() <= config.ItemSince
	}

	for _, item := range feed.Items[:itemSize] {
		if isInRange(item.PublishedParsed) {
			*work <- &WorkerRequest{
				Item:            item,
				LanguageCode:    feed.Language,
				Directory:       *direcory,
				UseNaturalVoice: config.UseNaturalVoice,
			}
		}
	}
}

// This code is taken from sample google TTS code with some modification
// Source: https://cloud.google.com/text-to-speech/docs/libraries
func speechSynthesizeWorker(wg *sync.WaitGroup, client *texttospeech.Client, workerItems *chan *WorkerRequest, ctx context.Context) error {
	defer wg.Done()

	for workerItem := range *workerItems {
		feedItem := workerItem.Item

		sanitizedTitle := strings.ReplaceAll(feedItem.Title, "/", "\\/")
		filename := sanitizedTitle + ".mp3"
		filepath, _ := filepath.Abs(workerItem.Directory + "/" + filename)

		if _, err := os.Stat(filepath); err == nil {
			log.Printf("File exists at path: %s\n, skip generating", filepath)
			return nil
		}

		log.Printf("Start procesing %v ", feedItem.Title)

		speechRequests := helper.GetSynthesizeSpeechRequests(feedItem, workerItem.LanguageCode, workerItem.UseNaturalVoice)
		audioContent := make([]byte, 0)

		for _, ssr := range speechRequests {
			resp, err := client.SynthesizeSpeech(ctx, ssr)
			if err != nil {
				log.Printf("err: %v\n", err)
				return err
			}

			audioContent = append(audioContent, resp.AudioContent...)
		}

		if err := os.WriteFile(filepath, audioContent, 0o644); err != nil {
			log.Printf("err: %v\n", err)
			return err
		}

		log.Printf("Finished Processing: %v, written to %v\n", feedItem.Title, filepath)
	}

	return nil
}
