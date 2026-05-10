package code

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/cli/cli/v2/internal/browser"
	"github.com/cli/cli/v2/internal/config"
	fd "github.com/cli/cli/v2/internal/featuredetection"
	"github.com/cli/cli/v2/internal/gh"
	ghmock "github.com/cli/cli/v2/internal/gh/mock"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/cli/cli/v2/pkg/search"
	"github.com/google/shlex"
	"github.com/stretchr/testify/assert"
)

func TestNewCmdCode(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		output  CodeOptions
		wantErr bool
		errMsg  string
	}{
		{
			name:    "no arguments",
			input:   "",
			wantErr: true,
			errMsg:  "specify search keywords or flags",
		},
		{
			name:  "keyword arguments",
			input: "react lifecycle",
			output: CodeOptions{
				Query: search.Query{
					Keywords: []string{"react", "lifecycle"},
					Kind:     "code",
					Limit:    30,
				},
			},
		},
		{
			name:  "web flag",
			input: "--web",
			output: CodeOptions{
				Query: search.Query{
					Keywords: []string{},
					Kind:     "code",
					Limit:    30,
				},
				WebMode: true,
			},
		},
		{
			name:  "limit flag",
			input: "--limit 10",
			output: CodeOptions{
				Query: search.Query{
					Keywords: []string{},
					Kind:     "code",
					Limit:    10,
				},
			},
		},
		{
			name:    "invalid limit flag",
			input:   "--limit 1001",
			wantErr: true,
			errMsg:  "`--limit` must be between 1 and 1000",
		},
		{
			name: "qualifier flags",
			input: `
			--extension=extension
			--filename=filename
			--match=path
			--language=language
			--repo=owner/repo
			--size=5
			--owner=owner
			`,
			output: CodeOptions{
				Query: search.Query{
					Keywords: []string{},
					Kind:     "code",
					Limit:    30,
					Qualifiers: search.Qualifiers{
						Extension: "extension",
						Filename:  "filename",
						In:        []string{"path"},
						Language:  "language",
						Repo:      []string{"owner/repo"},
						Size:      "5",
						User:      []string{"owner"},
					},
				},
			},
		},
		{
			name:    "multiple match values error",
			input:   "--match=path,file",
			wantErr: true,
			errMsg:  "--match accepts only one value for code search (either 'path' or 'file', not both)",
		},
		{
			name:  "owner qualifier",
			input: "--owner=microsoft",
			output: CodeOptions{
				Query: search.Query{
					Keywords: []string{},
					Kind:     "code",
					Limit:    30,
					Qualifiers: search.Qualifiers{
						User: []string{"microsoft"},
					},
				},
			},
		},
		{
			name:  "match path qualifier",
			input: "--match=path",
			output: CodeOptions{
				Query: search.Query{
					Keywords: []string{},
					Kind:     "code",
					Limit:    30,
					Qualifiers: search.Qualifiers{
						In: []string{"path"},
					},
				},
			},
		},
		{
			name:  "match file qualifier",
			input: "--match=file",
			output: CodeOptions{
				Query: search.Query{
					Keywords: []string{},
					Kind:     "code",
					Limit:    30,
					Qualifiers: search.Qualifiers{
						In: []string{"file"},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ios, _, _, _ := iostreams.Test()
			f := &cmdutil.Factory{
				IOStreams: ios,
			}
			argv, err := shlex.Split(tt.input)
			assert.NoError(t, err)
			var gotOpts *CodeOptions
			cmd := NewCmdCode(f, func(opts *CodeOptions) error {
				gotOpts = opts
				return nil
			})
			cmd.SetArgs(argv)
			cmd.SetIn(&bytes.Buffer{})
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})

			_, err = cmd.ExecuteC()
			if tt.wantErr {
				assert.EqualError(t, err, tt.errMsg)
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tt.output.Query, gotOpts.Query)
			assert.Equal(t, tt.output.WebMode, gotOpts.WebMode)
		})
	}
}

func TestCodeRun(t *testing.T) {
	var query = search.Query{
		Keywords: []string{"map"},
		Kind:     "code",
		Limit:    30,
		Qualifiers: search.Qualifiers{
			Language: "go",
			Repo:     []string{"cli/cli"},
		},
	}
	tests := []struct {
		errMsg     string
		name       string
		opts       *CodeOptions
		tty        bool
		wantErr    bool
		wantStderr string
		wantStdout string
		wantBrowse string
	}{
		{
			name: "displays results tty",
			opts: &CodeOptions{
				Query: query,
				Searcher: &search.SearcherMock{
					CodeFunc: func(query search.Query) (search.CodeResult, error) {
						return search.CodeResult{
							IncompleteResults: false,
							Items: []search.Code{
								{
									Name: "context.go",
									Path: "context/context.go",
									Repository: search.Repository{
										FullName: "cli/cli",
									},
									TextMatches: []search.TextMatch{
										{
											Fragment: "\t}\n\n\tvar repos []*api.Repository\n\trepoMap := map[string]bool{}\n\n\tadd := func(r *api.Repository) {\n\t\tfn := ghrepo.FullName(r)",
											Matches: []search.Match{
												{
													Text:    "Map",
													Indices: []int{38, 41},
												},
												{
													Text:    "map",
													Indices: []int{45, 48},
												},
											},
										},
									},
								},
								{
									Name: "pr.go",
									Path: "pkg/cmd/pr/pr.go",
									Repository: search.Repository{
										FullName: "cli/cli",
									},
									TextMatches: []search.TextMatch{
										{

											Fragment: "\t\t\t$ gh pr create --fill\n\t\t\t$ gh pr view --web\n\t\t`),\n\t\tAnnotations: map[string]string{\n\t\t\t\"help:arguments\": heredoc.Doc(`\n\t\t\t\tA pull request can be supplied as argument in any of the following formats:\n\t\t\t\t- by number, e.g. \"123\";",
											Matches: []search.Match{
												{
													Text:    "map",
													Indices: []int{68, 71},
												},
											},
										},
									},
								},
							},
							Total: 2,
						}, nil
					},
				},
			},
			tty:        true,
			wantStdout: "\nShowing 2 of 2 results\n\ncli/cli context/context.go\n\trepoMap := map[string]bool{}\n\ncli/cli pkg/cmd/pr/pr.go\n\tAnnotations: map[string]string{\n",
		},
		{
			name: "displays results notty",
			opts: &CodeOptions{
				Query: query,
				Searcher: &search.SearcherMock{
					CodeFunc: func(query search.Query) (search.CodeResult, error) {
						return search.CodeResult{
							IncompleteResults: false,
							Items: []search.Code{
								{
									Name: "context.go",
									Path: "context/context.go",
									Repository: search.Repository{
										FullName: "cli/cli",
									},
									TextMatches: []search.TextMatch{
										{
											Fragment: "\t}\n\n\tvar repos []*api.Repository\n\trepoMap := map[string]bool{}\n\n\tadd := func(r *api.Repository) {\n\t\tfn := ghrepo.FullName(r)",
											Matches: []search.Match{
												{
													Text:    "Map",
													Indices: []int{38, 41},
												},
												{
													Text:    "map",
													Indices: []int{45, 48},
												},
											},
										},
									},
								},
								{
									Name: "pr.go",
									Path: "pkg/cmd/pr/pr.go",
									Repository: search.Repository{
										FullName: "cli/cli",
									},
									TextMatches: []search.TextMatch{
										{

											Fragment: "\t\t\t$ gh pr create --fill\n\t\t\t$ gh pr view --web\n\t\t`),\n\t\tAnnotations: map[string]string{\n\t\t\t\"help:arguments\": heredoc.Doc(`\n\t\t\t\tA pull request can be supplied as argument in any of the following formats:\n\t\t\t\t- by number, e.g. \"123\";",
											Matches: []search.Match{
												{
													Text:    "map",
													Indices: []int{68, 71},
												},
											},
										},
									},
								},
							},
							Total: 2,
						}, nil
					},
				},
			},
			tty:        false,
			wantStdout: "cli/cli:context/context.go: repoMap := map[string]bool{}\ncli/cli:pkg/cmd/pr/pr.go: Annotations: map[string]string{\n",
		},
		{
			name: "displays no results",
			opts: &CodeOptions{
				Query: query,
				Searcher: &search.SearcherMock{
					CodeFunc: func(query search.Query) (search.CodeResult, error) {
						return search.CodeResult{}, nil
					},
				},
			},
			wantErr: true,
			errMsg:  "no code results matched your search",
		},
		{
			name: "displays search error",
			opts: &CodeOptions{
				Query: query,
				Searcher: &search.SearcherMock{
					CodeFunc: func(query search.Query) (search.CodeResult, error) {
						return search.CodeResult{}, fmt.Errorf("error with query")
					},
				},
			},
			wantErr: true,
			errMsg:  "error with query",
		},
		{
			name: "opens browser for web mode tty",
			opts: &CodeOptions{
				Query: query,
				Searcher: &search.SearcherMock{
					URLFunc: func(query search.Query) string {
						return "https://github.com/search?type=code&q=map+repo%3Acli%2Fcli"
					},
				},
				WebMode: true,
			},
			tty:        true,
			wantStderr: "Opening https://github.com/search in your browser.\n",
			wantBrowse: "https://github.com/search?type=code&q=map+repo%3Acli%2Fcli",
		},
		{
			name: "opens browser for web mode notty",
			opts: &CodeOptions{
				Query: query,
				Searcher: &search.SearcherMock{
					URLFunc: func(query search.Query) string {
						return "https://github.com/search?type=code&q=map+repo%3Acli%2Fcli"
					},
				},
				WebMode: true,
			},
			wantBrowse: "https://github.com/search?type=code&q=map+repo%3Acli%2Fcli",
		},
		{
			name: "preserves filename and extension qualifiers for github.com web search",
			opts: &CodeOptions{
				Config: func() (gh.Config, error) { return config.NewBlankConfig(), nil },
				Query: search.Query{
					Keywords: []string{"map"},
					Kind:     "code",
					Limit:    30,
					Qualifiers: search.Qualifiers{
						Filename:  "testing",
						Extension: "go",
					},
				},
				Searcher: search.NewSearcher(nil, "github.com", &fd.DisabledDetectorMock{}),
				WebMode:  true,
			},
			wantBrowse: "https://github.com/search?q=map+extension%3Ago+filename%3Atesting&type=code",
		},
		{
			name: "preserves extension with dot prefix for github.com web search",
			opts: &CodeOptions{
				Config: func() (gh.Config, error) { return config.NewBlankConfig(), nil },
				Query: search.Query{
					Keywords: []string{"map"},
					Kind:     "code",
					Limit:    30,
					Qualifiers: search.Qualifiers{
						Filename:  "testing",
						Extension: ".cpp",
					},
				},
				Searcher: search.NewSearcher(nil, "github.com", &fd.DisabledDetectorMock{}),
				WebMode:  true,
			},
			wantBrowse: "https://github.com/search?q=map+extension%3A.cpp+filename%3Atesting&type=code",
		},
		{
			name: "does not convert filename and extension qualifiers for GHES web search",
			opts: &CodeOptions{
				Config: func() (gh.Config, error) {
					cfg := &ghmock.ConfigMock{
						AuthenticationFunc: func() gh.AuthConfig {
							authCfg := &config.AuthConfig{}
							authCfg.SetDefaultHost("example.com", "GH_HOST")
							return authCfg
						},
					}
					return cfg, nil
				},
				Query: search.Query{
					Keywords: []string{"map"},
					Kind:     "code",
					Limit:    30,
					Qualifiers: search.Qualifiers{
						Filename:  "testing",
						Extension: "go",
					},
				},
				Searcher: search.NewSearcher(nil, "example.com", &fd.DisabledDetectorMock{}),
				WebMode:  true,
			},
			wantBrowse: "https://example.com/search?q=map+extension%3Ago+filename%3Atesting&type=code",
		},
		{
			name: "preserves extension qualifier for API search on github.com",
			opts: &CodeOptions{
				Config: func() (gh.Config, error) { return config.NewBlankConfig(), nil },
				Query: search.Query{
					Keywords: []string{"map"},
					Kind:     "code",
					Limit:    30,
					Qualifiers: search.Qualifiers{
						Extension: "go",
					},
				},
				Searcher: &search.SearcherMock{
					CodeFunc: func(query search.Query) (search.CodeResult, error) {
						// Verify that extension is preserved
						assert.Equal(t, "", query.Qualifiers.Path)
						assert.Equal(t, "go", query.Qualifiers.Extension)
						return search.CodeResult{
							IncompleteResults: false,
							Items: []search.Code{
								{
									Name: "test.go",
									Path: "test.go",
									Repository: search.Repository{
										FullName: "test/repo",
									},
									TextMatches: []search.TextMatch{
										{
											Fragment: "test",
											Matches:  []search.Match{{Text: "test", Indices: []int{0, 4}}},
										},
									},
								},
							},
							Total: 1,
						}, nil
					},
				},
			},
			tty:        true,
			wantStdout: "\nShowing 1 of 1 results\n\ntest/repo test.go\n\ttest\n",
		},
		{
			name: "preserves filename qualifier for API search on github.com",
			opts: &CodeOptions{
				Config: func() (gh.Config, error) { return config.NewBlankConfig(), nil },
				Query: search.Query{
					Keywords: []string{"map"},
					Kind:     "code",
					Limit:    30,
					Qualifiers: search.Qualifiers{
						Filename: "package.json",
					},
				},
				Searcher: &search.SearcherMock{
					CodeFunc: func(query search.Query) (search.CodeResult, error) {
						// Verify that filename is preserved
						assert.Equal(t, "", query.Qualifiers.Path)
						assert.Equal(t, "package.json", query.Qualifiers.Filename)
						return search.CodeResult{
							IncompleteResults: false,
							Items: []search.Code{
								{
									Name: "package.json",
									Path: "package.json",
									Repository: search.Repository{
										FullName: "test/repo",
									},
									TextMatches: []search.TextMatch{
										{
											Fragment: "test",
											Matches:  []search.Match{{Text: "test", Indices: []int{0, 4}}},
										},
									},
								},
							},
							Total: 1,
						}, nil
					},
				},
			},
			tty:        true,
			wantStdout: "\nShowing 1 of 1 results\n\ntest/repo package.json\n\ttest\n",
		},
		{
			name: "preserves filename and extension qualifiers for API search on github.com",
			opts: &CodeOptions{
				Config: func() (gh.Config, error) { return config.NewBlankConfig(), nil },
				Query: search.Query{
					Keywords: []string{"lint"},
					Kind:     "code",
					Limit:    30,
					Qualifiers: search.Qualifiers{
						Filename:  "package",
						Extension: "json",
					},
				},
				Searcher: &search.SearcherMock{
					CodeFunc: func(query search.Query) (search.CodeResult, error) {
						// Verify that filename and extension are preserved
						assert.Equal(t, "", query.Qualifiers.Path)
						assert.Equal(t, "package", query.Qualifiers.Filename)
						assert.Equal(t, "json", query.Qualifiers.Extension)
						return search.CodeResult{
							IncompleteResults: false,
							Items: []search.Code{
								{
									Name: "package.json",
									Path: "package.json",
									Repository: search.Repository{
										FullName: "test/repo",
									},
									TextMatches: []search.TextMatch{
										{
											Fragment: "test",
											Matches:  []search.Match{{Text: "test", Indices: []int{0, 4}}},
										},
									},
								},
							},
							Total: 1,
						}, nil
					},
				},
			},
			tty:        true,
			wantStdout: "\nShowing 1 of 1 results\n\ntest/repo package.json\n\ttest\n",
		},
		{
			name: "preserves filename and extension qualifiers for GHES API search",
			opts: &CodeOptions{
				Config: func() (gh.Config, error) {
					cfg := &ghmock.ConfigMock{
						AuthenticationFunc: func() gh.AuthConfig {
							authCfg := &config.AuthConfig{}
							authCfg.SetDefaultHost("example.com", "GH_HOST")
							return authCfg
						},
					}
					return cfg, nil
				},
				Query: search.Query{
					Keywords: []string{"map"},
					Kind:     "code",
					Limit:    30,
					Qualifiers: search.Qualifiers{
						Filename:  "testing",
						Extension: "go",
					},
				},
				Searcher: &search.SearcherMock{
					CodeFunc: func(query search.Query) (search.CodeResult, error) {
						// Verify that filename and extension are preserved (same as github.com)
						assert.Equal(t, "testing", query.Qualifiers.Filename)
						assert.Equal(t, "go", query.Qualifiers.Extension)
						assert.Equal(t, "", query.Qualifiers.Path)
						return search.CodeResult{
							IncompleteResults: false,
							Items: []search.Code{
								{
									Name: "testing.go",
									Path: "testing.go",
									Repository: search.Repository{
										FullName: "test/repo",
									},
									TextMatches: []search.TextMatch{
										{
											Fragment: "test",
											Matches:  []search.Match{{Text: "test", Indices: []int{0, 4}}},
										},
									},
								},
							},
							Total: 1,
						}, nil
					},
				},
			},
			tty:        true,
			wantStdout: "\nShowing 1 of 1 results\n\ntest/repo testing.go\n\ttest\n",
		},
		{
			name: "preserves language qualifier for API search on github.com",
			opts: &CodeOptions{
				Config: func() (gh.Config, error) { return config.NewBlankConfig(), nil },
				Query: search.Query{
					Keywords: []string{"deque"},
					Kind:     "code",
					Limit:    30,
					Qualifiers: search.Qualifiers{
						Language: "python",
					},
				},
				Searcher: &search.SearcherMock{
					CodeFunc: func(query search.Query) (search.CodeResult, error) {
						// Verify that language qualifier is preserved
						assert.Equal(t, "python", query.Qualifiers.Language)
						assert.Equal(t, "", query.Qualifiers.Path)
						return search.CodeResult{
							IncompleteResults: false,
							Items: []search.Code{
								{
									Name: "deque.py",
									Path: "collections/deque.py",
									Repository: search.Repository{
										FullName: "python/cpython",
									},
									TextMatches: []search.TextMatch{
										{
											Fragment: "class deque:",
											Matches:  []search.Match{{Text: "deque", Indices: []int{6, 11}}},
										},
									},
								},
							},
							Total: 1,
						}, nil
					},
				},
			},
			tty:        true,
			wantStdout: "\nShowing 1 of 1 results\n\npython/cpython collections/deque.py\n\tclass deque:\n",
		},
		{
			name: "preserves language and filename qualifiers together",
			opts: &CodeOptions{
				Config: func() (gh.Config, error) { return config.NewBlankConfig(), nil },
				Query: search.Query{
					Keywords: []string{"test"},
					Kind:     "code",
					Limit:    30,
					Qualifiers: search.Qualifiers{
						Language: "go",
						Filename: "main.go",
					},
				},
				Searcher: &search.SearcherMock{
					CodeFunc: func(query search.Query) (search.CodeResult, error) {
						// Verify that both language and filename qualifiers are preserved
						assert.Equal(t, "go", query.Qualifiers.Language)
						assert.Equal(t, "main.go", query.Qualifiers.Filename)
						assert.Equal(t, "", query.Qualifiers.Path)
						return search.CodeResult{
							IncompleteResults: false,
							Items: []search.Code{
								{
									Name: "main.go",
									Path: "cmd/app/main.go",
									Repository: search.Repository{
										FullName: "example/app",
									},
									TextMatches: []search.TextMatch{
										{
											Fragment: "func test() {}",
											Matches:  []search.Match{{Text: "test", Indices: []int{5, 9}}},
										},
									},
								},
							},
							Total: 1,
						}, nil
					},
				},
			},
			tty:        true,
			wantStdout: "\nShowing 1 of 1 results\n\nexample/app cmd/app/main.go\n\tfunc test() {}\n",
		},
		{
			name: "includes owner qualifier in API search (converted to repo pattern)",
			opts: &CodeOptions{
				Config: func() (gh.Config, error) { return config.NewBlankConfig(), nil },
				Query: search.Query{
					Keywords: []string{"cli"},
					Kind:     "code",
					Limit:    30,
					Qualifiers: search.Qualifiers{
						User: []string{"microsoft"},
					},
				},
				Searcher: &search.SearcherMock{
					CodeFunc: func(query search.Query) (search.CodeResult, error) {
						// Verify that owner qualifier is converted to repo pattern
						assert.Equal(t, []string{"microsoft/*"}, query.Qualifiers.Repo)
						assert.Equal(t, []string(nil), query.Qualifiers.User)
						return search.CodeResult{
							IncompleteResults: false,
							Items: []search.Code{
								{
									Name: "test.go",
									Path: "test.go",
									Repository: search.Repository{
										FullName: "microsoft/repo",
									},
									TextMatches: []search.TextMatch{
										{
											Fragment: "test",
											Matches:  []search.Match{{Text: "test", Indices: []int{0, 4}}},
										},
									},
								},
							},
							Total: 1,
						}, nil
					},
				},
			},
			tty:        true,
			wantStdout: "\nShowing 1 of 1 results\n\nmicrosoft/repo test.go\n\ttest\n",
		},
		{
			name: "does not convert owner qualifier for GHES API search",
			opts: &CodeOptions{
				Config: func() (gh.Config, error) {
					cfg := &ghmock.ConfigMock{
						AuthenticationFunc: func() gh.AuthConfig {
							authCfg := &config.AuthConfig{}
							authCfg.SetDefaultHost("example.com", "GH_HOST")
							return authCfg
						},
					}
					return cfg, nil
				},
				Query: search.Query{
					Keywords: []string{"cli"},
					Kind:     "code",
					Limit:    30,
					Qualifiers: search.Qualifiers{
						User: []string{"microsoft"},
					},
				},
				Searcher: &search.SearcherMock{
					CodeFunc: func(query search.Query) (search.CodeResult, error) {
						// Verify that owner qualifier is NOT converted for GHES
						assert.Equal(t, []string{"microsoft"}, query.Qualifiers.User)
						assert.Equal(t, []string(nil), query.Qualifiers.Repo)
						return search.CodeResult{
							IncompleteResults: false,
							Items: []search.Code{
								{
									Name: "test.go",
									Path: "test.go",
									Repository: search.Repository{
										FullName: "microsoft/repo",
									},
									TextMatches: []search.TextMatch{
										{
											Fragment: "test",
											Matches:  []search.Match{{Text: "test", Indices: []int{0, 4}}},
										},
									},
								},
							},
							Total: 1,
						}, nil
					},
				},
			},
			tty:        true,
			wantStdout: "\nShowing 1 of 1 results\n\nmicrosoft/repo test.go\n\ttest\n",
		},
		{
			name: "includes match path qualifier in API search",
			opts: &CodeOptions{
				Query: search.Query{
					Keywords: []string{"error"},
					Kind:     "code",
					Limit:    30,
					Qualifiers: search.Qualifiers{
						In: []string{"path"},
					},
				},
				Searcher: &search.SearcherMock{
					CodeFunc: func(query search.Query) (search.CodeResult, error) {
						// Verify that match path qualifier is included
						assert.Equal(t, []string{"path"}, query.Qualifiers.In)
						return search.CodeResult{
							IncompleteResults: false,
							Items: []search.Code{
								{
									Name: "test.go",
									Path: "test.go",
									Repository: search.Repository{
										FullName: "test/repo",
									},
									TextMatches: []search.TextMatch{
										{
											Fragment: "test",
											Matches:  []search.Match{{Text: "test", Indices: []int{0, 4}}},
										},
									},
								},
							},
							Total: 1,
						}, nil
					},
				},
			},
			tty:        true,
			wantStdout: "\nShowing 1 of 1 results\n\ntest/repo test.go\n\ttest\n",
		},
	}

	for _, tt := range tests {
		ios, _, stdout, stderr := iostreams.Test()
		ios.SetStdinTTY(tt.tty)
		ios.SetStdoutTTY(tt.tty)
		ios.SetStderrTTY(tt.tty)
		tt.opts.IO = ios
		browser := &browser.Stub{}
		tt.opts.Browser = browser
		t.Run(tt.name, func(t *testing.T) {
			err := codeRun(tt.opts)
			if tt.wantErr {
				assert.EqualError(t, err, tt.errMsg)
				return
			} else if err != nil {
				t.Fatalf("codeRun unexpected error: %v", err)
			}
			assert.Equal(t, tt.wantStdout, stdout.String())
			assert.Equal(t, tt.wantStderr, stderr.String())
			browser.Verify(t, tt.wantBrowse)
		})
	}
}
