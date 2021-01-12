package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/manifoldco/promptui"
	"github.com/yinheli/sshw"
)

const prev = "-parent-"

var (
	Build  = "devel"
	V      = flag.Bool("version", false, "show version")
	H      = flag.Bool("help", false, "show help")
	S      = flag.Bool("s", false, "use local ssh config '~/.ssh/config'")
	SZ     = flag.String("sz", "", "send file by path")
	RZ     = flag.String("rz", "", "download file by remote path")
	Output = flag.String("o", "", "dest for send or get file,default:/root/sshwtmp")
	NAME   = flag.String("n", "", "choose by node name")

	log = sshw.GetLogger()

	templates = &promptui.SelectTemplates{
		Label:    "✨ {{ . | green}}",
		Active:   "➤ {{ .Name | cyan  }}{{if .Alias}}({{.Alias | yellow}}){{end}} {{if .Host}}{{if .User}}{{.User | faint}}{{`@` | faint}}{{end}}{{.Host | faint}}{{end}}",
		Inactive: "  {{.Name | faint}}{{if .Alias}}({{.Alias | faint}}){{end}} {{if .Host}}{{if .User}}{{.User | faint}}{{`@` | faint}}{{end}}{{.Host | faint}}{{end}}",
	}
)

func findAlias(nodes []*sshw.Node, nodeAlias string) *sshw.Node {
	for _, node := range nodes {
		if node.Name == nodeAlias {
			return node
		}
		if len(node.Children) > 0 {
			n := findAlias(node.Children, nodeAlias)
			if n != nil {
				return n
			}
		}
	}
	return nil
}

func main() {
	flag.Parse()
	if !flag.Parsed() {
		flag.Usage()
		return
	}

	if *H {
		flag.Usage()
		return
	}

	if *V {
		fmt.Println("sshw - ssh client wrapper for automatic login")
		fmt.Println("  git version:", Build)
		fmt.Println("  go version :", runtime.Version())
		return
	}
	if *S {
		err := sshw.LoadSshConfig()
		if err != nil {
			log.Error("load ssh config error", err)
			os.Exit(1)
		}
	} else {
		err := sshw.LoadConfig()
		if err != nil {
			log.Error("load config error", err)
			os.Exit(1)
		}
	}

	var client sshw.Client
	// login by alias
	if *NAME != "" {
		var nodeAlias = *NAME
		var nodes = sshw.GetConfig()
		var node = findAlias(nodes, nodeAlias)
		if node != nil {
			client = sshw.NewClient(node)
		} else {
			log.Error("cannot find node by alias")
			os.Exit(1)
		}
	} else {
		node := choose(nil, sshw.GetConfig())
		if node == nil {
			log.Error("cannot get node")
			os.Exit(1)
		}
		client = sshw.NewClient(node)
	}
	if *SZ != "" {
		client.SendFile(*SZ, *Output)
	} else if *RZ != "" {
		client.GetFile(*RZ, *Output)
	} else {
		client.Shell()
	}
}

func choose(parent, trees []*sshw.Node) *sshw.Node {
	prompt := promptui.Select{
		Label:        "select host",
		Items:        trees,
		Templates:    templates,
		Size:         20,
		HideSelected: true,
		Searcher: func(input string, index int) bool {
			node := trees[index]
			content := fmt.Sprintf("%s %s %s", node.Name, node.User, node.Host)
			if strings.Contains(input, " ") {
				for _, key := range strings.Split(input, " ") {
					key = strings.TrimSpace(key)
					if key != "" {
						if !strings.Contains(content, key) {
							return false
						}
					}
				}
				return true
			}
			if strings.Contains(content, input) {
				return true
			}
			return false
		},
	}
	index, _, err := prompt.Run()
	if err != nil {
		return nil
	}

	node := trees[index]
	if len(node.Children) > 0 {
		first := node.Children[0]
		if first.Name != prev {
			first = &sshw.Node{Name: prev}
			node.Children = append(node.Children[:0], append([]*sshw.Node{first}, node.Children...)...)
		}
		return choose(trees, node.Children)
	}

	if node.Name == prev {
		if parent == nil {
			return choose(nil, sshw.GetConfig())
		}
		return choose(nil, parent)
	}

	return node
}
