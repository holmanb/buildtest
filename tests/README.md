# Buildtest Integration Tests

This directory holds a suite of integration tests for end-to-end tests of things like buildtest run. They use the standard go test runner, but are only executed if you set the integration build constraint.

## Run Tests

```bash
go test -count=1 -tags=integration ./tests/
```

The above command will build Buildtest first, then run tests with it.

To use an existing Buildtest binary rather than building one, you can explicitly set the flag `-buildtestbin`. For example, the following command will use a pre-built Buildtest at `/home/ubuntu/buildtest`:

```bash
go test -v -count=1 -tags=integration ./tests -buildtestbin=/home/ubuntu/buildtest
```

## Developing

### Visual Studio Code Settings

For VSCode Go and the gopls extention to work properly with files containing build tags, add the following:

```json
{
    "gopls": {
        "build.buildFlags": [
            "-tags=integration"
        ]
    }
}
```
