package main

import (
	"fmt"

	config "github.com/CodyCline/octotrident/pkg/config"
	"github.com/CodyCline/octotrident/pkg/runner"
	"github.com/logrusorgru/aurora"
	log "github.com/sirupsen/logrus"
)

var Version string

func main() {
	cfg, err := config.NewCLIConfig()
	if err != nil {
		log.Error(err)
		log.Fatal("an error occured with the configuration provided check your CLI arguments and try again")
	}
	if cfg.Version {
		fmt.Println(Version)
		log.Exit(0)
	}

	if cfg.Verbose {
		log.SetLevel(log.DebugLevel)
	}

	if cfg.Silent {
		log.SetLevel(log.FatalLevel)
	}

	//Dont show banner if silent
	if !cfg.Silent {
		fmt.Printf("%s\n", aurora.Green("banner todo").String())
	}

	log.Infof("current octotrident version %s", Version)
	fmt.Println(cfg.BatchSize)
	runner, err := runner.NewRunner(runner.RunnerOpts{
		ApiKeys:      cfg.ConfigFile.GitHub.APIKeys,
		Threads:      cfg.Threads,
		Timeout:      cfg.Timeout,
		Retries:      cfg.Retries,
		MaxForks:     cfg.MaxForks,
		BatchSize:    cfg.BatchSize,
		ShortShaSize: cfg.ShortShaSize,
	})
	if err != nil {
		log.Fatal(err)
	}

	results, err := runner.Init(cfg.Repository, cfg.Path)
	if err != nil {
		log.Error(err)
		log.Fatal("an error occured while cloning the repository")
	}
	log.Info("resolving all forks")
	runner.ResolveForks(results)

	log.Info("starting brute force of repository")
	runner.Start(results)
	log.Infof("finished brute force found %d hidden commits", len(results.HiddenCommits))
	runner.Finish(results)
	log.Infof("finished brute force found %d hidden commits", len(results.HiddenCommits))
}
