# Physical view

Where the processes run and how they scale. The Kubernetes manifests are not in the repository yet; this
view describes the target deployment the code is built for, and the local setup that runs today.

## Kubernetes

```mermaid
flowchart TB
    edge(["edge reverse proxy<br/>(TLS, HSTS)"])
    subgraph ns["application namespace (Pod Security: restricted)"]
        ing["Ingress controller"]
        subgraph stateless["stateless tiers"]
            web["web<br/>Deployment x2"]
            api["api<br/>Deployment x2"]
            md["media-delivery<br/>Deployment, HPA 2-5"]
        end
        disp["dispatcher<br/>Deployment x1-2<br/>ServiceAccount: jobs only"]
        watcher["watcher<br/>CronJob */5"]
        tj["transcoder<br/>Job per movie<br/>no ServiceAccount token"]
        pg[("Postgres<br/>one pod, ReadWriteOnce volume")]
        pv[("media volume<br/>NFS, ReadWriteMany")]
    end
    edge --> ing
    ing -- "/" --> web
    ing -- "/api" --> api
    ing -- "/media" --> md
    api --> pg
    disp --> pg
    disp -- "create / delete Job" --> tj
    tj --> pg
    watcher --> pg
    md -- "read-only" --> pv
    tj -- "read-write" --> pv
    watcher -- "read-write" --> pv
```

| Workload | Kind | Replicas | Storage | Probes |
|---|---|---|---|---|
| web | Deployment (nginx serving the built SPA) | 2 | none | HTTP probe on `8080` |
| api | Deployment | 2 | none | `/healthz`, `/readyz` (Postgres) |
| media-delivery | Deployment + HPA | 2–5 | media volume, read-only | `/healthz`, `/readyz` (the mount is readable) |
| dispatcher | Deployment | 1–2 | none | `/healthz`, `/readyz` (Postgres) |
| watcher | CronJob, every 5 min | one run at a time is enough; overlapping runs are safe | media volume | none: logs and exit code |
| transcoder | Job, created by the dispatcher | ≤ `MAX_CONCURRENT` | media volume | none: logs and exit code |
| Postgres | one pod (Postgres 16) | 1 | ReadWriteOnce volume | the database's own readiness check |

### Why the tiers are stateless

All state is in Postgres, so web, api and media-delivery can be scaled, rolled or restarted
freely. media-delivery in particular checks URL signatures with a shared key
(`secure_link_md5`), never calls the api, holds no session and writes nothing. It is the tier
that scales horizontally under viewer load. It serves static files only: no live transcoding,
ever. See [ADR 0001](decisions/0001-pre-transcoded-hls.md) and
[ADR 0003](decisions/0003-signed-urls-stateless-delivery.md).

### Liveness vs readiness

- `/healthz` is **liveness**: the process answers. It checks no dependency. If it checked
  Postgres, one database blip would make Kubernetes restart every api pod at once, and the
  restart would fix nothing.
- `/readyz` is **readiness**: the dependencies this pod needs to serve. Failing it takes the
  pod out of the Service until it recovers, without a restart. Movies already playing keep
  playing, because media-delivery never needs the database.

### Jobs, permissions and the shared volume

- The **dispatcher** is the only component with RBAC: create, get, list and delete on `jobs` in
  its namespace. The api has no Kubernetes permission at all.
- A **transcoder Job** is built in code, so everything in its spec comes from the dispatcher's
  environment:
  - resources;
  - a dedicated ServiceAccount with no RBAC and no mounted token;
  - `backoffLimit: 0`, a 6 h `activeDeadlineSeconds`, `ttlSecondsAfterFinished`;
  - the image pinned by digest;
  - `DATABASE_URL` from a `secretKeyRef`, never copied into the spec.
- The **media volume** is NFS, `ReadWriteMany`: transcoders and the watcher write it,
  media-delivery mounts it read-only. There are no `hostPath` volumes (Pod Security `restricted`
  rejects them).
- **Containers**:
  - non-root, all capabilities dropped, read-only root filesystem;
  - every HTTP container listens on `8080` (named port `http`);
  - images pinned to a patch version, plus the digest in manifests;
  - secrets reach the cluster encrypted (SOPS) and are applied by GitOps (Flux).

### The request chain and client addresses

A request passes an edge reverse proxy (TLS, HSTS), a tunnel and the cluster's ingress
controller before it reaches the api. Each hop appends what it saw to `X-Forwarded-For`.
`TRUSTED_PROXIES` lists the edge proxy's address as the cluster sees it and the pod CIDR; the
ingress controller must also be configured to trust the edge proxy, or it drops the header. See
[ADR 0009](decisions/0009-client-ip-from-trusted-proxies.md).

## Local development

```mermaid
flowchart LR
    dev(["browser"]) --> vite["Vite dev server<br/>127.0.0.1:5173"]
    vite -- "/api" --> api["make run<br/>api :8080"]
    vite -- "/media" --> md["make media-up<br/>nginx :8081"]
    api --> pg[("make db-up<br/>Postgres :5432")]
    disp["make dispatch<br/>local backend"] -- "child process" --> tr["transcoder"]
    scan["make scan<br/>watcher"] --> pg
    disp --> pg
    tr --> tree[("var/media")]
    md --> tree
    scan --> tree
```

Everything listens on `127.0.0.1` only: Postgres and nginx are published on loopback by
docker compose, and the api defaults to `127.0.0.1:8080`. The local dispatcher backend starts
the transcoder as a child process instead of a Kubernetes Job.
