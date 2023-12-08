# MetalBond Protocol v1

## Packet Format

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


## Message Types

| Type ID | Message Type |
|---------|--------------|
| 1       | HELLO        |
| 2       | KEEPALIVE    |
| 3       | SUBSCRIBE    |
| 4       | UNSUBSCRIBE  |
| 5       | UPDATE       |
