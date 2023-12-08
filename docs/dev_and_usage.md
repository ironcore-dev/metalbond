# Local development and testing

Metalbond contains command line tools to execute its components as a regular process, although it is mainly used by other applications as a library, such as [metalnet](https://github.com/ironcore-dev/metalnet).

## Start as a server
To start metalbond in the server mode that is able to distribute route updates, use the following command:

```
metalbond server < --listen connection_listening_IPv6:connection_listening_port > < --http display_web_listening_Ipv6:display_web_listening_port > [ --keepalive value ]
```

One example of this command can look like:

```sh
metalbond server --listen [::1]:4711 --http [::1]:4712 --keepalive 3
```

## Start as a client
For the development and testing purpose, metalbond can be started in the client mode to initiate route updates. For example, if a route needs to be announced to the network, the following command can be used:

```
 metalbond client      --server "[::]:4711"      --keepalive 5      --subscribe 100      --announce 100#1.2.3.4/32#abcd:efgh:1234::5
```

## Install Routes onto Linux

To install routes on a Linux system, you first need to create an IP-in-IPv6 tunnel endpoint:
```sh
ip link add overlay-tun type ip6tnl mode any external
ip link set up dev overlay-tun
```

and then start the metalbond client with the `--install-routes` command line parameter. This parameter requires a mapping between VNI and Kernel route table IDs. E.g. if you want to write all routes associated with VNI 23 to the Kernel route table 100, set `--install-routes 23#100`.
Set the tunnel device using the `tun` parameter:

```
./metalbond client --install-routes 23#100 --tun overlay-tun
```
