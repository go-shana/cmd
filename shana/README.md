# The `shana` command line

**Shana is under active development and in a very early stage (in POC phase). Don't use it in any production environment.**

The `shana` command line tool is to create, build and debug Shana microservice.

## Installation

The `shana` is built with Go. To install it, you need to have Go installed first. Then run the following command:

```bash
go install github.com/go-shana/cmd/shana@latest
```

## Create and run a new Shana microservice

To create a new Shana microservice, run the following command:

```bash
shana create example.com/service/go-foo
```

The `example.com/service/foo` is the module name of the new microservice. The `shana create` command will create a new directory named `go-foo` in the current directory and generate some files for start.

The directory structure is like this:

```bash
./go-foo
  ├── go.mod      # The Go module file
  ├── go.sum      # The Go module checksum file
  ├── shana.yaml  # A sample Shana configuration file.
  └── welcome.go  # A sample rpc API handler.
```

To start the microservice, enter the `go-foo` directory and run following command:

```bash
cd go-foo
shana run httpjson
```

By default, the `shana run` command will start a HTTP server on port 9696. You can use `curl` to test the microservice:

```bash
$ curl http://localhost:9696/welcome?name=Huan
{"data":{"message":"Hello, Huan"}}
```

The `shana.yaml` is the configuration file to control the behavior of the microservice. You can change the port number in the configuration file and restart the microservice to see the change.

For more details about developing Shana microservices, please refer to [Shana core package](https://github.com/go-shana/core).

## License

The `shana` command line tool is licensed under the MIT License. See [LICENSE](LICENSE) for the full license text.
