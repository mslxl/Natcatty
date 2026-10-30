// Command devseed fills a fresh Vault database with sample Host Groups,
// Hosts, Snippets, and Proxy Profiles, for looking at the interface with
// realistic content. Point it at a throwaway user-data directory:
//
//	go run ./internal/tools/devseed <userData>/Natcatty/natcatty.db
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/mslxl/natcatty/adapters/store"
	appvault "github.com/mslxl/natcatty/application/vault"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

func main() {
	if len(os.Args) != 2 {
		log.Fatal("usage: devseed <natcatty.db>")
	}
	ctx := context.Background()
	database, err := store.Open(ctx, os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	defer database.Close()
	vault := appvault.NewService(database,
		appvault.WithGroups(database),
		appvault.WithSnippets(database),
		appvault.WithProxyProfiles(database))

	for _, path := range []string{"Production", "Production/Databases", "Staging", "Lab"} {
		if _, err := vault.CreateGroup(ctx, domainvault.GroupDraft{Path: path}); err != nil {
			log.Printf("group %s: %v", path, err)
		}
	}
	hosts := []domainvault.Draft{
		{Label: "web-01", Hostname: "10.0.1.11", Username: "deploy", Group: "Production", Tags: []string{"web"}},
		{Label: "web-02", Hostname: "10.0.1.12", Username: "deploy", Group: "Production", Tags: []string{"web"}},
		{Label: "postgres-primary", Hostname: "10.0.2.21", Username: "postgres", Group: "Production/Databases", Tags: []string{"db"}},
		{Label: "redis-cache", Hostname: "10.0.2.31", Username: "root", Group: "Production/Databases"},
		{Label: "staging-app", Hostname: "staging.example.com", Port: 2222, Username: "ubuntu", Group: "Staging"},
		{Label: "k8s-control", Hostname: "192.168.50.10", Username: "admin", Group: "Lab", Pinned: true},
		{Label: "raspberry-pi", Hostname: "192.168.50.42", Username: "pi", Group: "Lab"},
		{Label: "bastion", Hostname: "bastion.example.com", Username: "ops", Pinned: true},
		{Label: "router", Hostname: "192.168.1.1", Username: "admin", Protocol: domainvault.ProtocolTelnet, Port: 23},
		{Label: "nas", Hostname: "192.168.1.20", Username: "admin"},
	}
	for _, draft := range hosts {
		if draft.Port == 0 {
			draft.Port = 22
		}
		if draft.Protocol == "" {
			draft.Protocol = domainvault.ProtocolSSH
		}
		if _, err := vault.Create(ctx, draft); err != nil {
			log.Printf("host %s: %v", draft.Label, err)
		}
	}
	for _, snippet := range []domainvault.SnippetDraft{
		{Label: "Disk usage", Command: "df -h"},
		{Label: "Top memory", Command: "ps aux --sort=-%mem | head -n 15"},
		{Label: "Restart nginx", Command: "sudo systemctl restart nginx"},
	} {
		if _, err := vault.CreateSnippet(ctx, snippet); err != nil {
			log.Printf("snippet %s: %v", snippet.Label, err)
		}
	}
	fmt.Println("seeded", os.Args[1])
}
