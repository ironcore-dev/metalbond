# MetalBond

[![REUSE status](https://api.reuse.software/badge/github.com/ironcore-dev/metalbond)](https://api.reuse.software/info/github.com/ironcore-dev/metalbond)
[![Go Report Card](https://goreportcard.com/badge/github.com/ironcore-dev/metalbond)](https://goreportcard.com/report/github.com/ironcore-dev/metalbond)
[![GitHub License](https://img.shields.io/static/v1?label=License&message=Apache-2.0&color=blue)](LICENSE)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](https://makeapullrequest.com)

## MetalBond Protocol v1

### Packet Format

MetalBond packets are transferred via TCP ontop of IPv6. The default TCP port is 4711.

     0                   1                   2                   3
     0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
    
    +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
    |    Version    |    Msg. Length (Big Endian)   |   Msg. Type   |
    +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
    |                                                               |
    |               Variable-Length Protobuf Message                |
    |                              ...                              |
    |                       (max 1188 bytes)                        |
    |                                                               |
    +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+

* `Version` must be 1.
* `Msg. Length` defines the length of the following `Variable-Length Protobuf Message`.
* `Msg. Type` identifies the type of the following Protobuf Message.

The metalbond protocol relies on IPv6 transport. Therefore we assume a minimum MTU of 1280 bytes. To prevent fragmentation all metalbond messages must not be larger than 1220 bytes overall (40 bytes IPv6 header, 20 bytes TCP header) - i.e. the variable-length protobuf message must not be longer than 1188 bytes.


### Message Types

| Type ID | Message Type |
|---------|--------------|
| 1       | HELLO        |
| 2       | KEEPALIVE    |
| 3       | SUBSCRIBE    |
| 4       | UNSUBSCRIBE  |
| 5       | UPDATE       |


#### HELLO Message


#### KEEPALIVE Message


#### SUBSCRIBE Message


#### UNSUBSCRIBE Message


#### UPDATE Message

## Install Routes

To install routes on a Linux system, you first need to create an IP-in-IPv6 tunnel endpoint:

    ip link add overlay-tun type ip6tnl mode any external
    ip link set up dev overlay-tun

and then start the metalbond client with the `--install-routes` command line parameter. This parameter requires a mapping between VNI and Kernel route table IDs. E.g. if you want to write all routes associated with VNI 23 to the Kernel route table 100, set `--install-routes 23#100`.
Set the tunnel device using the `tun` parameter:

    ./metalbond client --install-routes 23#100 --tun overlay-tun

## License

MetalBond is licensed under [Apache v2.0](LICENSE) - Copyright by the IronCore authors.
