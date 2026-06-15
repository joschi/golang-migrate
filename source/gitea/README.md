# gitea

`gitea://user:personal-access-token@gitea_host/owner/repo/path?scheme=https#ref`

Unauthenticated access is also supported:

`gitea://gitea_host/owner/repo/path?scheme=https#ref`

| URL Query | WithInstance Config | Description |
|-----------|---------------------|-------------|
| user | | (optional) The username of the user connecting |
| personal-access-token | | (optional) Personal access token from the Gitea instance |
| gitea_host | | Hostname of the Gitea server |
| owner | `Owner` | Repo owner |
| repo | `Repo` | Repo name |
| path | `Path` | Path in repo to migrations |
| ref | `Ref` | (optional) SHA, branch, or tag |
| scheme | | (optional) `https` by default, `http` for local instances |
