# git-remote-arweave

A git remote helper that stores repositories on the [Arweave](https://arweave.org) blockchain. Push and fetch using standard git commands with `arweave://` URLs. No intermediary services, no platform fees beyond Arweave's storage cost.

```
git remote add origin arweave://<your-wallet-address>/my-project
git push origin main
git clone arweave://<wallet-address>/my-project
```

The canonical repository lives on Arweave itself. GitHub is a read-only mirror.

```
arweave://JBw0K8Fw7aIIDmvJepH3Aa7hapVhxUwVkzbzL24_dBw/git-remote-arweave
```

## Table of contents

- [How it works](#how-it-works)
- [Installation](#installation)
- [Usage](#usage)
  - [Create a new repository](#create-a-new-repository-on-arweave)
  - [Clone](#clone-an-existing-repository)
  - [Push](#push-changes)
  - [Fork](#fork-a-repository)
  - [Merge requests](#merge-requests)
  - [Check status](#check-transaction-status)
- [Configuration](#configuration)
  - [Gateways](#gateways)
- [Payment](#payment)
  - [Top up Turbo credits](#top-up-turbo-credits)
  - [How much does a push cost?](#how-much-does-a-push-cost)
- [Private repositories](#private-repositories)
  - [Creating a private repository](#creating-a-private-repository)
  - [Managing readers](#managing-readers)
  - [Converting between public and private](#converting-between-public-and-private)
- [Limitations](#limitations)
- [Design](#design)
- [Project structure](#project-structure)
- [Local development](#local-development)
- [Support](#support)

## How it works

Every `git push` uploads two Arweave transactions:

1. **Pack transaction** -- a git packfile containing the new objects
2. **Ref manifest transaction** -- a JSON document mapping refs to commit SHAs and listing all pack transaction IDs

Private repos additionally upload a **keymap transaction** (encrypted symmetric keys wrapped for each authorized reader) on the first push and whenever the reader list changes. Otherwise the manifest references the existing keymap.

To find the current state of a repository, the client queries the Arweave GraphQL gateway for the latest ref manifest tagged with the owner's wallet and repository name.

On `git fetch`, the client compares the manifest's pack list against a local set of already-applied packs, downloads only the new ones, and applies them.

## Installation

```sh
curl -fsSL https://raw.githubusercontent.com/git-remote-arweave/git-remote-arweave/refs/heads/main/install.sh | bash
```

Downloads a temporary Go toolchain, builds from source, installs to `~/.local/bin/`. No root required. Works on Linux and macOS (amd64/arm64).

Make sure `~/.local/bin` is in your `PATH`. Git discovers remote helpers by looking for `git-remote-<scheme>` executables.

### Build from source manually

Requires Go 1.22+:

```sh
git clone https://github.com/git-remote-arweave/git-remote-arweave
cd git-remote-arweave
make install
```

Installs to `$(go env GOPATH)/bin/`.

## Usage

### Create a new repository on Arweave

```sh
cd my-project
git init && git add . && git commit -m "Initial commit"
git remote add origin arweave://<your-wallet-address>/<repo-name>
git push origin main
```

The first push automatically creates the genesis manifest. No separate init step is needed.

### Clone an existing repository

```sh
git clone arweave://<wallet-address>/<repo-name>
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

### Fork a repository

```sh
git clone arweave://<original-owner>/<repo-name>
cd repo-name
git remote set-url origin arweave://<your-wallet-address>/<repo-name>
git push origin main
```

The push detects that you cloned from a different owner and creates a fork: the genesis manifest references the original packs (no re-upload) and includes a `Forked-From` tag pointing to the source manifest. For private repositories, the fork owner must be an authorized reader of the original -- epoch keys are inherited and re-wrapped for the fork's own reader set if it differs from the original, or the source keymap is reused as-is.

When forking a private repo as public, encrypted source packs are re-uploaded without encryption and the `Forked-From` tag is omitted — the result is an independent public repo that does not reference or expose the source repo's keys. An interactive confirmation prompt asks you to type the repository name before proceeding.

### Merge requests

Merge requests propose merging changes from a fork into the target repository. They are Arweave transactions -- immutable, permissionless, and encrypted for private repos. Updates (close, reopen, merge) form a chain of transactions linked via `Parent-Tx`. The chain is tamper-proof: only the MR author and the target repo owner can post valid updates, and data that fails validation is silently discarded.

#### Create a merge request

From your fork's working directory:

```sh
arweave-git mr create -m "Add feature X" --source-ref feature
```

- `--target` defaults to the upstream repo (resolved from the `Forked-From` tag on the fork's genesis manifest). Use `--target <owner>/<repo>` to override.
- `--target-ref` defaults to `main`.
- `--source-ref` defaults to the current branch.
- Without `-m`, opens `$EDITOR`.

#### List merge requests

```sh
# Incoming (as target repo owner)
arweave-git mr list

# Outgoing (as fork author)
arweave-git mr list --outgoing
```

#### View details

```sh
arweave-git mr view <tx-id>
```

Shows title, status, source/target refs, and the current source manifest (updated when the author pushes new commits).

#### Review changes

```sh
git fetch arweave://<source-owner>/<source-repo> <source-ref>
git diff main...FETCH_HEAD
```

#### Push new commits

Just push to your fork — `git push` detects all outgoing merge requests for the pushed branch and updates their source manifest refs on Arweave accordingly, so the target owner will see the latest code when viewing or merging:

```sh
git push origin main
```

#### Update merge request description

```sh
arweave-git mr update <tx-id> -m "New description replacing the original"
```

#### Close / reopen

```sh
arweave-git mr close <tx-id>
arweave-git mr reopen <tx-id> -m "New description replacing the original"
```

Either the author or the target repo owner can close. Reopening accepts `-m` to replace the merge request description.

#### Merge

```sh
# Fetch, merge, push, and mark as merged (opens $EDITOR for commit message)
arweave-git mr merge <tx-id>

# With a commit message (skips $EDITOR)
arweave-git mr merge <tx-id> -m "Merge feature X"

# Accept default merge commit message
arweave-git mr merge <tx-id> --no-edit

# Force merge commit (no fast-forward)
arweave-git mr merge <tx-id> --no-ff

# Squash merge
arweave-git mr merge <tx-id> --squash

# Just mark as merged (manual workflow)
arweave-git mr merge <tx-id> --no-merge
```

Only the target repo owner can merge. Once merged, the status is terminal -- it cannot be reopened.

### Check transaction status

```sh
arweave-git status
```

Shows the settlement status of Arweave transactions reachable from the current HEAD manifest via parent-tx chain walk. For each manifest, pack, and keymap transaction, checks:

- **TGW** -- data available via the Turbo fetch gateway (shown only when using Turbo payment)
- **GW** -- data available via the main gateway (arweave.net) HEAD request
- **GQL** -- transaction indexed in GraphQL (fully settled on L1)

By default shows the latest 10 manifests. Use `--all` to show the full chain.

Transient gateway errors (502/503/504) are shown as `?` rather than ✗.

## Configuration

Configuration is resolved in priority order: environment variable > git config > default.

| Parameter | Env var | Git config | Default |
|---|---|---|---|
| Wallet keyfile path | `ARWEAVE_WALLET` | `arweave.wallet` | -- (required for push) |
| Gateway URL | `ARWEAVE_GATEWAY` | `arweave.gateway` | `https://arweave.net` |
| Payment method | `ARWEAVE_PAYMENT` | `arweave.payment` | `turbo` |
| Turbo upload URL | `ARWEAVE_TURBO_GATEWAY` | `arweave.turboGateway` | `https://upload.ardrive.io` |
| Fetch gateway URL | `ARWEAVE_FETCH_GATEWAY` | `arweave.fetchGateway` | (see below) |
| Visibility | `ARWEAVE_VISIBILITY` | `arweave.visibility` | `public` |
| Drop timeout | `ARWEAVE_DROP_TIMEOUT` | `arweave.dropTimeout` | `30m` |

The wallet is an Arweave JWK keyfile (JSON). It is required for push operations and for fetching private repositories (decryption). Public repos can be cloned and fetched without a wallet.

```sh
# Set wallet globally
git config --global arweave.wallet /path/to/wallet.json

# Or per-repo
git config arweave.wallet /path/to/wallet.json

# Or via environment
export ARWEAVE_WALLET=/path/to/wallet.json
```

### Gateways

The client uses three separate gateway endpoints, each serving a different role:

- **Gateway** (`arweave.gateway`) -- the main Arweave gateway. Used for GraphQL queries (finding manifests, looking up repos), transaction status checks, and as a fallback data source. Default: `https://arweave.net`.

- **Fetch gateway** (`arweave.fetchGateway`) -- used to download transaction data (packfiles, manifests, keymaps). When using Turbo, defaults to `https://turbo-gateway.com`, which serves bundled data items within seconds of upload. The main gateway (`arweave.net`) only serves bundled data after L1 settlement, which can take minutes to hours -- so it cannot be used as a reliable fallback for Turbo-uploaded data. When using native L1 payment, defaults to the main gateway (no bundling involved).

- **Turbo gateway** (`arweave.turboGateway`) -- the Turbo upload endpoint. Used only for push when `arweave.payment = turbo`. Accepts ANS-104 data items via HTTP. Default: `https://upload.ardrive.io`.

In most cases, you don't need to configure any of these -- the defaults work out of the box. Override them if you're using a custom gateway, running arlocal, or debugging connectivity issues.

## Payment

By default, uploads go through [ArDrive Turbo](https://ardrive.io) -- a bundling service that accepts payment in SOL, ETH, MATIC, USDC, AR, ARIO, or fiat (credit card via Stripe). Turbo works on a **prepaid credit** model: you top up your balance, and each push deducts from it. Delivery is guaranteed once the upload succeeds.

The alternative is **native L1** upload (`git config arweave.payment native`), which sends transactions directly to the Arweave network and requires AR tokens in the wallet. L1 transactions have a minimum size granularity of 256 KiB, so small pushes (typical for git) cost orders of magnitude more than via Turbo. Native L1 is primarily used for local development with [arlocal](https://github.com/textury/arlocal).

### Top up Turbo credits

Install the Turbo CLI:

```sh
npm install -g @ardrive/turbo-sdk
```

Check your balance:

```sh
turbo balance --address <your-wallet-address> --token arweave
```

Top up with crypto:

```sh
# With SOL
turbo crypto-fund --value 0.05 --token solana --wallet-file /path/to/solana-wallet.json

# With ETH
turbo crypto-fund --value 0.01 --token ethereum --private-key <eth-private-key>

# With AR
turbo crypto-fund --value 0.1 --token arweave --wallet-file /path/to/arweave-wallet.json
```

Top up with fiat (opens Stripe checkout in the browser):

```sh
turbo top-up --address <your-wallet-address> --currency USD --value 5
```

Check how much storage you can get:

```sh
turbo price --value 5 --type usd
turbo token-price --byte-count 10485760 --token solana
```

### How much does a push cost?

Typical git pushes produce packfiles of 1--100 KB plus a small JSON manifest. Turbo subsidizes uploads under 100 KiB, so most regular pushes are **free**. Larger pushes cost a fraction of a cent -- you can push hundreds of times on $1 of credits.

## Private repositories

Repositories can be encrypted so that only authorized readers can clone or fetch.

### Creating a private repository

```sh
git config arweave.visibility private
git push origin main
```

On the first push with `visibility = private`, a symmetric encryption key is generated and stored locally in `.git/arweave/encryption.json`. All pack data and the manifest body are encrypted with NaCl secretbox (XSalsa20-Poly1305). The symmetric key is wrapped with the owner's RSA public key (from the Arweave wallet) and uploaded as a separate **keymap** transaction.

Only authorized readers (including the owner) can decrypt the data. The keymap transaction is referenced from the manifest via a public `Key-Map` tag, so the fetch logic can locate it without decrypting anything first.

### Managing readers

Readers are wallet addresses authorized to decrypt the repository. The owner's address is always included automatically. Reader management is done locally -- changes take effect on the next push.

```sh
# Add a reader (pubkey will need to be fetched from Arweave)
arweave-git readers add <wallet-address>

# Add a reader with their RSA public key (base64url-encoded modulus)
# Use this when the reader hasn't transacted on Arweave yet
arweave-git readers add <wallet-address> --pubkey <base64url-modulus>

# Remove a reader
arweave-git readers remove <wallet-address>

# List current readers
arweave-git readers list
```

The `--pubkey` flag accepts the RSA modulus (`n` field from the Arweave JWK wallet) encoded in base64url. This is the same value as the `owner` field in Arweave transactions. The reader can extract it from their wallet keyfile and share it with the repo owner.

When a reader is **added**, the existing encryption keys are re-wrapped for the new reader and the keymap is re-uploaded on the next push.

When a reader is **removed**, a new encryption epoch is created with a fresh symmetric key. The removed reader retains access to packs encrypted before their removal (Arweave data is immutable), but cannot decrypt any future pushes.

### Converting between public and private

**Public to private:** Set `arweave.visibility` to `private` and push. If there are no new commits, create an empty commit first (`git commit --allow-empty -m "convert to private"`). Future packs will be encrypted. Historical unencrypted packs remain publicly accessible -- converting to private does not retroactively hide data.

**Private to public:** Set `arweave.visibility` to `public` and push (empty commit if needed). An interactive confirmation prompt asks you to type the repository name before proceeding. An **open keymap** is uploaded where all epoch keys are stored in plaintext, allowing anyone to decrypt historical encrypted packs. Future packs are uploaded unencrypted.

## Limitations

- **No deletion.** Data on Arweave is permanent. Force-push orphans old data but cannot erase it.
- **Confirmation latency.** Pushed data becomes visible in minutes, not seconds. Not suitable for workflows requiring instant collaboration.
- **Storage cost.** Pushes may cost Turbo credits or AR tokens. See [How much does a push cost?](#how-much-does-a-push-cost).
- **Gateway dependence.** Fetching requires an accessible Arweave gateway.
- **Metadata is not encrypted.** Tags on private repos are always public: repo name, owner address, transaction type, parent tx link, timestamps, and the `Visibility: private` flag. The keymap reveals reader addresses and number of epochs. An observer can see that a private repo exists, who owns it, who can read it, and how often it is updated -- without seeing the content.
- **No retroactive revocation.** Removing a reader does not revoke access to previously encrypted data (Arweave is immutable). Converting public to private does not hide already-uploaded unencrypted data.
- **Reader key discovery.** Reader public keys must be either discoverable on Arweave (requires at least one prior transaction) or shared off-chain and added with `arweave-git readers add --pubkey`.

## Design

**Single owner.** Each repository has exactly one wallet that can push. The `(wallet-address, repo-name)` pair *is* the repository identity, and only the wallet owner can sign transactions for it. Multi-writer would require an on-chain access control layer (smart contracts, token gating, or a consensus protocol), adding complexity and attack surface for a problem git already solves: fork the repo, push to your own copy, send a merge request. This is how the Linux kernel has scaled to thousands of contributors without shared write access to a single repository.

**Tamper-proof identity.** Repositories are identified by the `(wallet-address, repo-name)` pair. Every upload is cryptographically signed by the owner's wallet. With native L1 transactions, the wallet address is derived from the signature by the Arweave network itself. With Turbo (ANS-104 data items), the signature is verified by the gateway when indexing the bundle. In both cases, it is cryptographically impossible to forge a transaction from someone else's wallet.

**Pending push handling.** Arweave transaction confirmation takes minutes. After uploading, the client stores a pending state locally (`.git/arweave/`) including a copy of the packfile. On the next push, the client checks confirmation status: if confirmed, it promotes the state; if dropped (not found after a timeout), it re-uploads from the local copy. When using Turbo, delivery is guaranteed and re-upload never happens. This means `git push` returns in seconds.

**Immutability.** Once pushed, data is permanent. `force-push` creates a new manifest that ignores old data, but the old transactions remain on Arweave forever. Accidentally pushed secrets cannot be removed.

**Encryption.** Private repositories use NaCl secretbox (XSalsa20-Poly1305) for symmetric encryption and RSA-OAEP (SHA-256) for key wrapping, using the Arweave wallet's RSA keys directly. An epoch-based key rotation scheme ensures that removed readers lose access to future data while the system remains simple and stateless -- no on-chain access control, no smart contracts.

## Project structure

```
cmd/
  git-remote-arweave/    # remote helper entry point (invoked by git)
  arweave-git/           # companion CLI (status, readers, env)
internal/
  config/                # configuration loading
  crypto/                # encryption (NaCl secretbox, RSA-OAEP key wrapping, keymap)
  manifest/              # ref manifest types, JSON, tag constants
  pack/                  # packfile generation and application (go-git)
  arweave/               # Arweave client (L1 upload, Turbo upload, fetch, GraphQL)
  localstate/            # .git/arweave/ state management
  ops/                   # push/fetch/pending business logic
  helper/                # git remote helper protocol (stdin/stdout)
```

## Local development

Local development and testing use [arlocal](https://github.com/textury/arlocal) -- a local Arweave gateway emulator. It supports transactions, GraphQL queries, manual block mining, and token minting. Zero cost, instant confirmation, no network required.

arlocal uses native L1 transactions, so set `arweave.payment` to `native`:

```sh
git config arweave.payment native
git config arweave.gateway http://localhost:1984
```

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
export ARWEAVE_PAYMENT=native
ADDR=<address-from-previous-step>

# Build
make build
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

## Support

If you find this project useful, consider sending AR to `JBw0K8Fw7aIIDmvJepH3Aa7hapVhxUwVkzbzL24_dBw` to cover maintenance, development and transaction fee costs.

## License

[Apache License 2.0](LICENSE)
