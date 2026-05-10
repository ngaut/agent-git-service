package search

import (
	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/spf13/cobra"

	searchCodeCmd "github.com/cli/cli/v2/pkg/cmd/search/code"
	searchCommitsCmd "github.com/cli/cli/v2/pkg/cmd/search/commits"
	searchIssuesCmd "github.com/cli/cli/v2/pkg/cmd/search/issues"
	searchPrsCmd "github.com/cli/cli/v2/pkg/cmd/search/prs"
	searchReposCmd "github.com/cli/cli/v2/pkg/cmd/search/repos"
)

func NewCmdSearch(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "search <command>",
		Short: "Search for repositories, issues, and pull requests",
		Long: heredoc.Docf(`
			Search across all of GitHub.

			Excluding search results that match a qualifier

			In a browser, the GitHub search syntax supports excluding results that match a search qualifier
			by prefixing the qualifier with a hyphen. For example, to search for issues that
			do not have the label "bug", you would use %[1]s-label:bug%[1]s as a search qualifier.

			%[1]sgh%[1]s supports this syntax in %[1]sgh search%[1]s as well, but it requires extra
			command line arguments to avoid the hyphen being interpreted as a command line flag because it begins with a hyphen.

			On Unix-like systems, you can use the %[1]s--%[1]s argument to indicate that
			the arguments that follow are not a flag, but rather a query string. For example:

			$ gh search issues -- "my-search-query -label:bug"

			On PowerShell, you must use both the %[1]s--%[2]s%[1]s argument and the %[1]s--%[1]s argument to
			produce the same effect. For example:

			$ gh --%[2]s search issues -- "my search query -label:bug"

			See the following for more information:
			- GitHub search syntax: <https://docs.github.com/en/search-github/getting-started-with-searching-on-github/understanding-the-search-syntax#exclude-results-that-match-a-qualifier>
			- The PowerShell stop parse flag %[1]s--%[2]s%[1]s: <https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.core/about/about_parsing?view=powershell-7.5#the-stop-parsing-token>
			- The Unix-like %[1]s--%[1]s argument: <https://www.gnu.org/software/bash/manual/bash.html#Shell-Builtin-Commands-1>
		`, "`", "%"),
	}

	cmd.AddCommand(searchCodeCmd.NewCmdCode(f, nil))
	cmd.AddCommand(searchCommitsCmd.NewCmdCommits(f, nil))
	cmd.AddCommand(searchIssuesCmd.NewCmdIssues(f, nil))
	cmd.AddCommand(searchPrsCmd.NewCmdPrs(f, nil))
	cmd.AddCommand(searchReposCmd.NewCmdRepos(f, nil))

	return cmd
}
