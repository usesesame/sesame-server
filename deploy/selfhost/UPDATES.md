# Publishing the self-host update feed

This page is for the maintainers who sign and publish the feed that self-hosted servers read. Operators read [Update notices](../../SELF-HOSTING.md#update-notices). The route that serves the feed is not part of this repository. A feed is one static file at an https address, so any static host works.

## What the feed is

The file is a JSON envelope `{payload, signature, keyId}`. `payload` is unpadded base64url JSON. `signature` is an unpadded base64url Ed25519 signature over the bytes `sesame-update-feed-v1`, a line feed and the decoded payload bytes. The prefix stops a signature made for some other purpose from being replayed as a feed. `keyId` names one of the public keys pinned in the server.

The payload looks like this. All values here are made up.

```json
{
  "schemaVersion": 1,
  "sequence": 12,
  "issuedAt": "2026-11-01T09:00:00Z",
  "expiresAt": "2026-11-29T09:00:00Z",
  "products": [
    {
      "id": "sesame-server",
      "channels": {
        "stable": {
          "version": "0.2.0",
          "publishedAt": "2026-10-30T12:00:00Z",
          "notesUrl": "https://example.net/releases/0.2.0",
          "security": false,
          "minimumFrom": "0.1.0",
          "images": [{"ref": "registry.example.net/sesame/server@sha256:0000000000000000000000000000000000000000000000000000000000000000"}],
          "binaries": [{"os": "linux", "arch": "amd64", "url": "https://downloads.example.net/sesame-server-linux-amd64", "sha256": "1111111111111111111111111111111111111111111111111111111111111111"}]
        }
      }
    }
  ]
}
```

A server refuses the feed unless every rule in the Updates section of [the API description](../../internal/selfhost/server/API.md) holds. The ones that catch mistakes are these: `expiresAt` is at most 45 days after `issuedAt`, `sequence` is a whole number from 1 to 1,000,000,000, every URL is https, every `sha256` is 64 lowercase hex digits, every image ref is a lowercase name followed by `@sha256:` and 64 lowercase hex digits, and `sesame-server` is present. `stable` never offers a prerelease version. Products and channels that the server does not know are ignored, so a new product is added by registering its id and channels in `internal/selfhost/updates/registry.go` and listing it in the feed.

## Sign offline

Do this on a machine that is not connected to a network, with the private key on removable storage.

1. Build the tool from a checkout of the release you trust: `go build -o feedsign ./cmd/feedsign`.
2. Make a key pair: `./feedsign keygen -id sesame-update-1 -out /media/keys/sesame-update-1.key`. The private key goes into the file with mode 0600 and is never printed. The command prints the public entry to pin, in the form `sesame-update-1:<base64url key>`. Keep the file off the build machines and out of the repository.
3. Build the release with that entry. The release workflow reads the repository variable `SESAME_UPDATE_PUBLIC_KEYS` and passes it as a build argument, and an empty variable is allowed and leaves the release reporting `not_configured`. For a local image, pass `--build-arg SESAME_UPDATE_PUBLIC_KEYS=sesame-update-1:<base64url key>` to `docker build -f Dockerfile.selfhost`. For a plain binary, pass `-ldflags "-X usesesame.app/backend/internal/buildinfo.UpdatePublicKeys=sesame-update-1:<base64url key>"` to `go build`.
4. Write the payload, then sign it: `./feedsign sign -key /media/keys/sesame-update-1.key -sequence 12 -previous 11 -in feed.json -out selfhost.json`. The tool refuses a key file that is a symbolic link or readable by others, refuses a sequence above one billion, and with `-previous` refuses a sequence lower than that number or more than 1000 above it. It checks the payload against the same rules the server applies, refuses a payload that is already expired or issued in the future, and verifies its own output. It will not overwrite an existing output file.
5. Check the result as a server would: `./feedsign verify -pub sesame-update-1:<base64url key> selfhost.json`.
6. Copy `selfhost.json` to the machine that serves it.

## Sequence numbers

Every server remembers the highest sequence it has accepted for each signing key and refuses anything lower from that key. Use a number higher than every feed you signed with that key before, and pass it as `-previous` so the tool catches a typo. A key never has to continue another key's numbers, so a rotated key can start again at 1. Signing the same number again is safe only for the same content, because servers accept an equal number as the same document. If you publish a number that is too high by mistake, every server that accepted it refuses any lower number from that key afterwards. Operators can clear that with `sesame-server updates reset-sequence`, which forgets every key's highest number and is written to the audit log. Keep a record of the last number you published outside the repository.

## Publishing and renewal

Serve `selfhost.json` over https with `Content-Type: application/json`. Keep it under 256 KiB. Do not redirect to another address, because servers refuse a redirect that leaves the origin. Servers show `feed_expired` once `expiresAt` has passed, so sign a new feed with a higher sequence well before then, for example every two weeks with a 45 day lifetime.

## Rotating the key

Pinned keys are a list. Ship a release that pins the old and the new key, wait until servers have updated, then sign feeds with the new key. A server built with only the old key refuses feeds signed by the new one and shows `feed_invalid`, so do not drop the old key from a release before most servers have moved on. Operators can also set `SESAME_UPDATE_PUBLIC_KEYS` themselves, and a value set there replaces the keys compiled into the build.
