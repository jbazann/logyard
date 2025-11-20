
# Logyard

Logyard is a minimalist, drop-in solution for managing and viewing logs. It offers a simple middle ground between heavy log stacks and command-line interfaces.

## Downloads

TODO

## Overview

Logyard provides a small binary (currently ~8MB) that can be deployed in the same environment as your application. 

It's primary functionality is running a lightweight server over HTTP, providing access to log files under a single endpoint. Additionally, Logyard implements different execution modes, which can be orchestrated to further simplify monitoring complex architectures.

### Execution modes

Logyard aims to be an all-in-one solution, more modes are likely to be added as the project evolves.

#### Server mode (standard) 

Starts a lightweight server that listens for HTTP requests (at `localhost:23212` by default, subject to change), listing all `*.log` files it can find (see configuration section for `-src` for details about scanned locations). Each file can then be streamed through WebSockets.

Logyard runs in **server mode** when no other modes are enabled.

#### Capture mode
 
Dumps any input received through `STDIN` into a log file. In general, a regular pipe into a file is a more straightforward way to feed the server, but **capture mode** provides enhancements such as rolling logs (starting a new file after reaching a certain size) and a stable target directory.

Enable **capture mode** with `--capture` or `-c`. 

#### Demo mode

Prints logs to `STDERR`, simulating a real application. This mode can be useful to test complex setups and confirm that logs are reaching the server.

To toggle **demo mode**, use `--demo <n>` where `<n>` is the number of lines to print before terminating. A value of `0` runs indefinitely. 

## Configuration

Logyard provides a number of configurable options. These are subject to change, so they are currently only available as command-line arguments. They can be listed using `logyard -h`.

Support for environment variables and configuration files will be added "eventually".

## Project status

Only a prototype is available at the moment. 

This repository includes a demo script for PowerShell, though it's easy to replicate in any platform. Just start a `demo mode` instance and pipe its output into a `capture mode` one or directly into a file in the same directory as the binary, then run `./logyard -src app://`. 

## License

TODO

### Temporary license

Do whatever you want with this code, provided:

If you use Logyard for data produced by artifacts whose execution is profitable to you or someone you work for, you pinky-promise to donate a reasonable fraction of said profits to a charitable cause.

Consequences for violating this license are governed by standard "pinky-promise" rules.