package gitea

import (
	"context"
	"fmt"
	"io"
	"net/http"
	nurl "net/url"
	"os"
	"strings"

	sdk "gitea.dev/sdk"
	"github.com/golang-migrate/migrate/v4/source"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

func init() {
	source.Register("gitea", &Gitea{})
}

const DefaultScheme = "https"

var (
	ErrNoUserInfo      = fmt.Errorf("no username:token provided")
	ErrNoAccessToken   = fmt.Errorf("no access token")
	ErrInvalidHost     = fmt.Errorf("invalid host")
	ErrInvalidRepo     = fmt.Errorf("invalid repo")
	ErrInvalidScheme   = fmt.Errorf("invalid scheme")
	ErrNoDir           = fmt.Errorf("no directory")
	ErrInvalidResponse = fmt.Errorf("invalid response")
)

type Gitea struct {
	client *sdk.Client
	config *Config

	migrations *source.Migrations
}

type Config struct {
	Owner string
	Repo  string
	Path  string
	Ref   string
}

func (g *Gitea) Open(ctx context.Context, url string) (source.Driver, error) {
	u, err := nurl.Parse(url)
	if err != nil {
		return nil, err
	}

	if u.Host == "" {
		return nil, ErrInvalidHost
	}

	scheme := u.Query().Get("scheme")
	if scheme == "" {
		scheme = DefaultScheme
	}
	if scheme != "http" && scheme != "https" {
		return nil, ErrInvalidScheme
	}

	httpClient := http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport)}
	options := []sdk.ClientOption{sdk.SetHTTPClient(&httpClient)}

	if u.User != nil {
		password, ok := u.User.Password()
		if !ok {
			return nil, ErrNoAccessToken
		}
		options = append(options, sdk.SetToken(password))
	}

	client, err := sdk.NewClient(fmt.Sprintf("%s://%s", scheme, u.Host), options...)
	if err != nil {
		return nil, err
	}

	gn := &Gitea{
		client:     client,
		migrations: source.NewMigrations(),
	}

	pe := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(pe) < 2 {
		return nil, ErrInvalidRepo
	}

	gn.config = &Config{
		Owner: pe[0],
		Repo:  pe[1],
		Ref:   u.Fragment,
	}
	if len(pe) > 2 {
		gn.config.Path = strings.Join(pe[2:], "/")
	}

	if err := gn.readDirectory(ctx); err != nil {
		return nil, err
	}

	return gn, nil
}

func WithInstance(ctx context.Context, client *sdk.Client, config *Config) (source.Driver, error) {
	gn := &Gitea{
		client:     client,
		config:     config,
		migrations: source.NewMigrations(),
	}
	if err := gn.readDirectory(ctx); err != nil {
		return nil, err
	}
	return gn, nil
}

func (g *Gitea) readDirectory(ctx context.Context) error {
	g.ensureFields()

	if err := ctx.Err(); err != nil {
		return err
	}

	if g.config == nil || g.config.Owner == "" || g.config.Repo == "" {
		return ErrInvalidRepo
	}

	if err := g.resolveRef(ctx); err != nil {
		return err
	}

	nodes, _, err := g.client.Repositories.ListContents(ctx, g.config.Owner, g.config.Repo, g.config.Ref, g.config.Path)
	if err != nil {
		if strings.Contains(err.Error(), "expect directory, got file") {
			return ErrNoDir
		}
		return err
	}

	for i := range nodes {
		m, err := g.nodeToMigration(nodes[i])
		if err != nil {
			continue
		}
		if !g.migrations.Append(m) {
			return fmt.Errorf("unable to parse file %v", nodes[i].Name)
		}
	}

	return nil
}

func (g *Gitea) resolveRef(ctx context.Context) error {
	if g.config.Ref != "" {
		return nil
	}

	repo, _, err := g.client.Repositories.GetRepo(ctx, g.config.Owner, g.config.Repo)
	if err != nil {
		return err
	}
	if repo == nil || repo.DefaultBranch == "" {
		return ErrInvalidRepo
	}

	g.config.Ref = repo.DefaultBranch
	return nil
}

func (g *Gitea) nodeToMigration(node *sdk.ContentsResponse) (*source.Migration, error) {
	m, err := source.DefaultParse(node.Name)
	if err != nil {
		return nil, err
	}
	m.Raw = node.Path
	return m, nil
}

func (g *Gitea) ensureFields() {
	if g.config == nil {
		g.config = &Config{}
	}
	if g.migrations == nil {
		g.migrations = source.NewMigrations()
	}
}

func (g *Gitea) Close(ctx context.Context) error {
	return nil
}

func (g *Gitea) First(ctx context.Context) (version uint, err error) {
	g.ensureFields()

	if v, ok := g.migrations.First(ctx); !ok {
		return 0, &os.PathError{Op: "first", Path: g.config.Path, Err: os.ErrNotExist}
	} else {
		return v, nil
	}
}

func (g *Gitea) Prev(ctx context.Context, version uint) (prevVersion uint, err error) {
	g.ensureFields()

	if v, ok := g.migrations.Prev(ctx, version); !ok {
		return 0, &os.PathError{Op: fmt.Sprintf("prev for version %v", version), Path: g.config.Path, Err: os.ErrNotExist}
	} else {
		return v, nil
	}
}

func (g *Gitea) Next(ctx context.Context, version uint) (nextVersion uint, err error) {
	g.ensureFields()

	if v, ok := g.migrations.Next(ctx, version); !ok {
		return 0, &os.PathError{Op: fmt.Sprintf("next for version %v", version), Path: g.config.Path, Err: os.ErrNotExist}
	} else {
		return v, nil
	}
}

func (g *Gitea) ReadUp(ctx context.Context, version uint) (r io.ReadCloser, identifier string, err error) {
	g.ensureFields()

	if m, ok := g.migrations.Up(ctx, version); ok {
		r, resp, err := g.client.Repositories.GetFileReader(ctx, g.config.Owner, g.config.Repo, g.config.Ref, m.Raw)
		if err != nil {
			return nil, "", err
		}
		if resp != nil && resp.StatusCode != http.StatusOK {
			return nil, "", ErrInvalidResponse
		}
		return r, m.Identifier, nil
	}
	return nil, "", &os.PathError{Op: fmt.Sprintf("read version %v", version), Path: g.config.Path, Err: os.ErrNotExist}
}

func (g *Gitea) ReadDown(ctx context.Context, version uint) (r io.ReadCloser, identifier string, err error) {
	g.ensureFields()

	if m, ok := g.migrations.Down(ctx, version); ok {
		r, resp, err := g.client.Repositories.GetFileReader(ctx, g.config.Owner, g.config.Repo, g.config.Ref, m.Raw)
		if err != nil {
			return nil, "", err
		}
		if resp != nil && resp.StatusCode != http.StatusOK {
			return nil, "", ErrInvalidResponse
		}
		return r, m.Identifier, nil
	}
	return nil, "", &os.PathError{Op: fmt.Sprintf("read version %v", version), Path: g.config.Path, Err: os.ErrNotExist}
}
