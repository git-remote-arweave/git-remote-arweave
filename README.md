# git-remote-arweave

A git remote helper that stores repositories on the [Arweave](https://arweave.org) blockchain. Push and fetch using standard git commands with `arweave://` URLs. No intermediary services, no tokens, no platform fees beyond Arweave's native storage cost.

```
git remote add origin arweave://<your-wallet-address>/my-project
git push origin main
git clone arweave://<wallet-address>/my-project
```

## How it works

Every `git push` uploads two Arweave transactions:

1. **Pack transaction** -- a git packfile containing the new objects
2. **Ref manifest transaction** -- a JSON document mapping refs to commit SHAs and listing all pack transaction IDs

To find the current state of a repository, the client queries the Arweave GraphQL gateway for the latest ref manifest tagged with the owner's wallet and repository name.

On `git fetch`, the client compares the manifest's pack list against a local set of already-applied packs, downloads only the new ones, and applies them.

## Key design decisions

**Single owner.** Each repository has exactly one wallet that can push. The wallet address in the URL *is* the repository identity -- only the holder of the corresponding private key can sign transactions. Multi-writer would require an on-chain access control layer (smart contracts, token gating, or a consensus protocol), adding complexity and attack surface for a problem git already solves: fork the repo, push to your own copy, send a merge request. This is how the Linux kernel has scaled to thousands of contributors without shared write access to a single repository.

**Tamper-proof identity.** Repositories are identified by the `(wallet-address, repo-name)` pair. The wallet address is derived from the transaction's cryptographic signature by the Arweave network itself, so repository ownership cannot be spoofed.

**Pending push handling.** Arweave transaction confirmation takes minutes. After uploading, the client stores a pending state locally (`.git/arweave/`) including a copy of the packfile. On the next push, the client checks confirmation status: if confirmed, it promotes the state; if dropped (not found after a timeout), it re-uploads from the local copy. This means `git push` returns in seconds.

**Immutability.** Once pushed, data is permanent. `force-push` creates a new manifest that ignores old data, but the old transactions remain on Arweave forever. Accidentally pushed secrets cannot be removed.

## Installation

Build from source (requires Go 1.22+):

```sh
git clone https://github.com/git-remote-arweave/git-remote-arweave
cd git-remote-arweave
make install
```

This builds the binary with version info from git tags and installs it to `$(go env GOPATH)/bin/`. Make sure this directory is in your `PATH`. Git discovers remote helpers by looking for `git-remote-<scheme>` executables.

## Configuration

Configuration is resolved in priority order: environment variable > git config > default.

| Parameter | Env var | Git config | Default |
|---|---|---|---|
| Wallet keyfile path | `ARWEAVE_WALLET` | `arweave.wallet` | -- (required for push) |
| Gateway URL | `ARWEAVE_GATEWAY` | `arweave.gateway` | `https://arweave.net` |
| Drop timeout | `ARWEAVE_DROP_TIMEOUT` | `arweave.dropTimeout` | `30m` |

The wallet is an Arweave JWK keyfile (JSON). It is only required for push operations; fetch and clone work without a wallet.

```sh
# Set wallet globally
git config --global arweave.wallet /path/to/wallet.json

# Or per-repo
git config arweave.wallet /path/to/wallet.json

# Or via environment
export ARWEAVE_WALLET=/path/to/wallet.json
```

## Usage

### Create a new repository on Arweave

```sh
cd my-project
git init && git add . && git commit -m "initial"
git remote add origin arweave://<your-wallet-address>/my-project
git push origin main
```

The first push automatically creates the genesis manifest. No separate init step is needed.

### Clone an existing repository

```sh
git clone arweave://<wallet-address>/repo-name
```

### Fetch updates

```sh
git fetch origin
```

### Push changes

```sh
git push origin main
```

After pushing, you'll see a message with the transaction IDs. The data becomes globally visible once the transactions confirm (typically a few minutes).

## Limitations

- **No deletion.** Data on Arweave is permanent. Force-push orphans old data but cannot erase it.
- **Confirmation latency.** Pushed data becomes visible in minutes, not seconds. Not suitable for workflows requiring instant collaboration.
- **Storage cost.** Every push costs AR tokens. Roughly $5--10 per GB at current rates. No free tier.
- **Gateway dependence.** Fetching requires an accessible Arweave gateway.

## Project structure

```
cmd/
  git-remote-arweave/    # remote helper entry point
internal/
  config/                # configuration loading
  manifest/              # ref manifest types, JSON, tag constants
  pack/                  # packfile generation and application (go-git)
  arweave/               # Arweave client (upload, fetch, GraphQL)
  localstate/            # .git/arweave/ state management
  ops/                   # push/fetch/pending business logic
  helper/                # git remote helper protocol (stdin/stdout)
```

## Local development

Local development and testing use [arlocal](https://github.com/textury/arlocal) -- a local Arweave gateway emulator. It supports transactions, GraphQL queries, manual block mining, and token minting. Zero cost, instant confirmation.

Requires Node.js 18+.

### Start arlocal

```sh
npx arlocal
```

This starts a local gateway on `http://localhost:1984`.

### Generate a test wallet and mint tokens

Install the [arweave](https://www.npmjs.com/package/arweave) npm package and use it to generate a wallet, derive its address, and mint tokens on arlocal:

```sh
npm install arweave
```

```sh
node -e "
const Arweave = require('arweave');
const fs = require('fs');
const arweave = Arweave.init({ host: 'localhost', port: 1984, protocol: 'http' });

(async () => {
  const key = await arweave.wallets.generate();
  fs.writeFileSync('wallet.json', JSON.stringify(key));
  const addr = await arweave.wallets.jwkToAddress(key);
  await arweave.api.get('/mint/' + addr + '/1000000000000');
  await arweave.api.get('/mine');
  console.log(addr);
})();
"
```

This prints the wallet address. Save it for use in remote URLs.

### Run against arlocal

```sh
export ARWEAVE_GATEWAY=http://localhost:1984
export ARWEAVE_WALLET=./wallet.json
ADDR=<address-from-previous-step>

# Build
go build -o git-remote-arweave ./cmd/git-remote-arweave/
export PATH="$PWD:$PATH"

# Push
cd my-test-repo
git remote add origin arweave://$ADDR/test-repo
git push origin main

# Mine to confirm
curl -s http://localhost:1984/mine

# Clone from another directory
cd /tmp
git clone arweave://$ADDR/test-repo
```

### Run tests

```sh
go test ./...
```

## Support

If you find this project useful, consider sending AR to `JBw0K8Fw7aIIDmvJepH3Aa7hapVhxUwVkzbzL24_dBw` to cover maintenance, development and transaction fee costs.

## License

[Apache License 2.0](LICENSE)