# PKT Lightning Wallet

[![ISC License](http://img.shields.io/badge/license-ISC-blue.svg)](http://Copyfree.org)
![Master branch build Status](https://github.com/pkt-cash/PKT-Lightning-Wallet/actions/workflows/go.yml/badge.svg?branch=master)
![Develop branch build Status](https://github.com/pkt-cash/PKT-Lightning-Wallet/actions/workflows/go.yml/badge.svg?branch=develop)

This repository contains the [PKT](https://pkt.cash) Lightning Wallet.
This is a *command line* wallet, for a GUI app which embeds this wallet's
code, consider using:

* https://pkt.world/wallet (Windows/Mac/Linux/Android)
* https://play.google.com/store/apps/details?id=co.anode.anodium.playstore (Android with VPN included)

This is a fully peer-to-peer wallet, so it does not rely on any support
servers, only the [PKT-FullNode](https://github.com/pkt-cash/PKT-FullNode)s.

This wallet can be used in normal wallet mode, or in experimental Lightning
mode.

* `pld` - The PKT Lightning Daemon, this is the main executable which runs the wallet. It is
controlled using an http RPC interface.
* `pldctl` - A lightweight command line application which allows you to send commands to `pld`
using the command line.

## Compiling

To build this wallet, you need [golang](https://go.dev/dl/) and the
[protocol buffers](https://command-not-found.com/protoc) compiler.

Using `git`, clone the project from the repository:

```bash
$ git clone https://github.com/pkt-cash/PKT-Lightning-Wallet
$ cd PKT-Lightning-Wallet
$ ./do
```

This will build `pld`, and `pldctl` inside of the `./bin` sub-folder.

### Advanced building

The `./do` build script invokes golang code to build golang code, so using normal environment
variables will affect the build code as well as the final code. To use environment variables and
affect the final code without the build code, place them *after* the `./do` command, not before.

Cross-compiling for windows on a Mac:

```bash
$ ./do GOOS=windows GOARCH=amd64
```

The script will only accept env vars if they begin with CAPITAL letters, numbers and the underscore
before an equal sign. So `MY_ENV_VAR=value` will be passed through as an environment variable, but
`my_env_var=value` will not.

Whatever does not match the env var pattern is treated as a command line flag for the go build.
For example `./do -tags dev` will run `go build` with `-tags dev` argument.

Finally, you can run the build manually, but you must run ./do first because some code is generated.
But generated code does not depend on the OS or architecture, so you can safely compile using
whatever tool you prefer, after you have run `./do` once.

## Creating a new wallet

Before you will be able to use `pld`, you will need to create a new wallet. You can do this manually
*or* programmatically.

```
user@armee PKT-Lightning-Wallet % ./bin/pld --create
Enter the private passphrase for your new wallet:
Confirm passphrase:
Do you have an existing wallet seed you want to use? (n/no/y/yes) [no]: no
Encrypting your seed...
Your wallet generation seed is:

tail actual message wave cook this is not an actual seed phrase long auto august

IMPORTANT: Keep the seed in a safe place.
If your wallet is destroyed, you can recover it as long as
you have this seed and your wallet passphrase.
Please keep in mind that anyone who has access
to the seed only needs to guess your wallet passphrase to
access your funds.
The seed is encrypted using your wallet passphrase
YOU MUST REMEMBER YOUR WALLET PASSPHRASE TO RESTORE FROM SEED.

Once you have stored the seed in a safe and secure location, type "OK" to continue: OK
Creating the wallet...
1704382099 [INF] wallet.go:3554 Opened wallet
The wallet has been created successfully.
user@armee PKT-Lightning-Wallet %
```

1. You will be prompted to enter a passphrase, this is the passphrase for your wallet, you MUST keep
it safe. If you lose it, you will lose access to your funds, and even the seed will not work without
the passphrase. **NOTE:** When you type, nothing will be shown back to you.
2. You will be asked to confirm your passphrase.
3. You will be asked if you have a wallet seed to recover from, if you answer yes then you'll be
prompted to enter the seed AND the passphrase used for the wallet which the seed came from.
4. Your new seed, protected by the newly created passphrase, will be printed. You MUST keep it safe,
this seed along with the passphrase can be used to recover the wallet if it is lost or corrupted.

### Creating a wallet programmatically

You can also create a wallet programmatically by piping a JSON structure into pld as follows:

```
echo '{"passphrase":"outscore deviancy palm spearmint"}' | ./bin/pld --create
```

Pld will reply with a json structure containing the seed words to save:

```json
{"seed":"outside twin torch this is not a real seed either dont write this one down"}
```

For recovering a wallet programmatically, you can additionally include a `"seed"` field and a
`"seedpassphrase"` field in the json object which you pass in.

### Creating a named wallet

Pld allows you to manage have wallet files, though you can only have one open at a time.
To create a named wallet, use the `--wallet` argument. The below example will create a wallet
called "personal", which corrisponds to a file called "wallet_personal.db" in the wallet folder.

```
./bin/pld --create --wallet personal
```

### Migrating from pktwallet

If you're upgrading from pktwallet to pld, you don't need to do anything special. Pld uses the
same wallet location and on-disk format as pktwallet.

## Statup

To launch pld, just run:

```
./bin/pld
```

If you want to launch with a specific wallet, you can pass `--wallet` with the name of the wallet
as you would do when creating the wallet.

## Documentation

The *primary* source of documentation is from the API help endpoint. To learn about how you can
manage your pld instance, start up pld and then in a separate window, run `./bin/pldctl`.

**NOTE:** You need to have pld running in order for pldctl to work, pldctl queries pld to know
what endpoints it has and how they can be called.

**NOTE2:** If the Lightning system has not been started ( `./bin/pldctl unlock --start_lightning` )
then some of the endpoints will be missing and you will not see help for them.

Each endpoint also has related help documentation which can be used to learn more about how to
call the endpoint and what it does. You can see this help by running pldctl with help and then
the specific endpoint.

```
user@armee PKT-Lightning-Wallet % ./bin/pldctl help wallet/transaction/create
  Create a transaction but do not send it to the chain
  This does not store the transaction as existing in the wallet so
  /wallet/transaction/query will not return a transaction created by this
  endpoint. In order to make multiple transactions concurrently, prior to
  the first transaction being submitted to the chain, you must specify the
  autolock field.

OPTIONS:
  --to_address=value - Address which we will be paying to
  --amount - Number of PKT to send
    Specify Infinity to send as much as possible in a single transaction.
  --from_address=value - Addresses which can be selected for sourcing funds from
  --electrum_format - Output an electrum format transaction, this format carries additional payload for enabling
    transactions to be signed off-line, including multi-signature.
  --change_address=value - If not empty-string, this address will be used for making change
  --input_min_height=value - Do not source funds from any transaction outputs less than this block height
  --min_conf=value - Do not source funds from any transaction outputs unless they have at least this many confirms
  --max_inputs=value - Do not make inputs to source funds from any more than this number of previous transaction outputs
  --autolock=value - Create a "named lock" for all outputs which are to be spent, this allows you to prevent further
    invocations of create transaction from trying to reference the same coins.
    The name is entirely your choice.
    The locked outputs will be unlocked on restart of the wallet, or by using the wallet/unspent/lock/create
    with unlock = true, or if the transaction is sent to the chain (in which case they become permanently
    unusable),
  --sign - Whether to sign the transaction.
user@armee PKT-Lightning-Wallet %
```

## API

Everything that can be done using pldctl can also be done programmatically using the HTTP api.
Below is an example of querying the help for the `wallet/transaction/create` endpoint.

```
user@armee PKT-Lightning-Wallet % curl http://localhost:53199/api/v1/help/wallet/transaction/create
{
        "path": "/api/v1/wallet/transaction/create",
        "description": [
                "Create a transaction but do not send it to the chain",
                "This does not store the transaction as existing in the wallet so",
                "/wallet/transaction/query will not return a transaction created by this",
                "endpoint. In order to make multiple transactions concurrently, prior to",
                "the first transaction being submitted to the chain, you must specify the",
                "autolock field."
        ],
        "request": {
                "name": "rpc_pb_CreateTransactionRequest",
                "description": [],
                "fields": [
                        {
                                "name": "to_address",
                                "description": [
                                        "Address which we will be paying to"
                                ],
                                "repeated": false,
                                "type": {
                                        "name": "string",
                                        "description": [],
                                        "fields": []
                                }
                        },
```

### OpenAPI

You can also get an OpenAPI YAML file generated from the live API by running:

```
./bin/pldctl openapi | jq -r .yaml
```

## License

`pktd` is licensed under the [Copyfree](http://Copyfree.org) ISC License.
