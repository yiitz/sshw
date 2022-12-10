package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/manifoldco/promptui"
	"github.com/yiitz/sshw/internal/client"
	"github.com/yiitz/sshw/internal/config"
	"github.com/yiitz/sshw/pkg/log"
)

const prev = "-parent-"

var (
	Build  = "devel"
	V      = flag.Bool("version", false, "show version")
	H      = flag.Bool("help", false, "show help")
	CMD    = flag.String("c", "", "execute command and exit")
	S      = flag.Bool("s", false, "use local ssh config '~/.ssh/config'")
	SZ     = flag.String("put", "", "upload file local path")
	RZ     = flag.String("get", "", "download file remote path")
	Output = flag.String("o", "", "file output path, default: ${cwd}/${fileName}")
	NAME   = flag.String("n", "", "choose by node name")

	logger = log.GetLogger()

	templates = &promptui.SelectTemplates{
		Label:    "✨ {{ . | green}}",
		Active:   "➤ {{ .Name | cyan  }}{{if .Alias}}({{.Alias | yellow}}){{end}} {{if .Host}}{{if .User}}{{.User | faint}}{{`@` | faint}}{{end}}{{.Host | faint}}{{end}}",
		Inactive: "  {{.Name | faint}}{{if .Alias}}({{.Alias | faint}}){{end}} {{if .Host}}{{if .User}}{{.User | faint}}{{`@` | faint}}{{end}}{{.Host | faint}}{{end}}",
	}
)

func findAlias(nodes []*config.Node, nodeAlias string) *config.Node {
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
		err := config.LoadSshConfig()
		if err != nil {
			logger.Error("load ssh config error", err)
			os.Exit(1)
		}
	} else {
		err := config.LoadConfig()
		if err != nil {
			logger.Error("load config error", err)
			os.Exit(1)
		}
	}

	var sshClient client.Client
	// login by alias
	if *NAME != "" {
		var nodeAlias = *NAME
		var nodes = config.GetConfig()
		var node = findAlias(nodes, nodeAlias)
		if node != nil {
			sshClient = client.NewClient(node)
		} else {
			logger.Error("cannot find node by alias")
			os.Exit(1)
		}
	} else {
		node := choose(nil, config.GetConfig())
		if node == nil {
			logger.Error("cannot get node")
			os.Exit(1)
		}
		sshClient = client.NewClient(node)
	}
	if *SZ != "" {
		sshClient.SendFile(*SZ, *Output)
	} else if *RZ != "" {
		sshClient.GetFile(*RZ, *Output)
	} else {
		sshClient.Shell(*CMD)
	}
}

func choose(parent, trees []*config.Node) *config.Node {
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
			first = &config.Node{Name: prev}
			node.Children = append(node.Children[:0], append([]*config.Node{first}, node.Children...)...)
		}
		return choose(trees, node.Children)
	}

	if node.Name == prev {
		if parent == nil {
			return choose(nil, config.GetConfig())
		}
		return choose(nil, parent)
	}

	return node
}
