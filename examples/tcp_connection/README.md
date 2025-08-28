# TCP Connection Example

This example demonstrates how to connect to VPP using TCP sockets instead of Unix domain sockets.

## Prerequisites

1. VPP must be configured to listen on a TCP socket. Add this to your VPP startup configuration:

```
socksvr {
    socket-name tcp:0.0.0.0:5002
}
```

Or start VPP with:
```bash
vpp unix { cli-listen /run/vpp/cli.sock } socksvr { socket-name tcp:0.0.0.0:5002 }
```

2. Restart VPP for the changes to take effect:
```bash
systemctl restart vpp
```

## Usage

### Basic TCP Connection
```bash
# Connect to VPP on localhost:5002 (default)
go run tcp_example.go

# Connect to VPP on a specific host and port
go run tcp_example.go -tcp 192.168.1.100:5002

# Use explicit scheme prefix
go run tcp_example.go -tcp 192.168.1.100:5002 -scheme
```

### Unix Socket Connection (backward compatibility)
```bash
# Connect via Unix socket (for comparison)
go run tcp_example.go -unix /run/vpp/api.sock

# With scheme prefix
go run tcp_example.go -unix /run/vpp/api.sock -scheme
```

## Connection String Formats

The enhanced GoVPP supports multiple connection string formats:

- `tcp://host:port` - TCP connection with explicit scheme
- `host:port` - TCP connection (auto-detected by presence of colon)
- `unix:///path/to/socket` - Unix socket with explicit scheme
- `/path/to/socket` - Unix socket (auto-detected by path format)

## Example Output

```
2024/01/15 10:23:45 Connecting to VPP via TCP: tcp://localhost:5002
2024/01/15 10:23:45 Connected to VPP

VPP Version Information:
  Program:        vpp
  Version:        23.10.0-release
  Build Date:     2024-01-10T12:00:00
  Build Directory:/build/vpp

VPP Interfaces:
===============
  [0] local0
      Admin: down, Link: down
      MTU: L3=0, IP4=0, IP6=0

  [1] GigabitEthernet0/8/0
      Admin: up, Link: up
      MAC: 08:00:27:12:34:56
      MTU: L3=9000, IP4=9000, IP6=9000
```

## Security Considerations

⚠️ **WARNING**: TCP connections are unencrypted and unauthenticated. For production use:

1. Use a secure network (VPN, private network)
2. Implement firewall rules to restrict access
3. Consider using SSH tunneling:
   ```bash
   ssh -L 5002:localhost:5002 user@vpp-host
   go run tcp_example.go -tcp localhost:5002
   ```
4. Future enhancement: Add TLS support to the TCP adapter

## Performance

Expected latencies:
- Unix Socket: ~50-100μs
- TCP (localhost): ~100-200μs  
- TCP (LAN): ~200-500μs
- TCP (WAN): >1ms

For best performance, use Unix sockets when possible. TCP is primarily useful for:
- Remote management
- Container/VM environments
- Development and testing
- Distributed systems

## Troubleshooting

### Connection Refused
- Verify VPP is running: `systemctl status vpp`
- Check VPP is listening: `netstat -tlnp | grep 5002`
- Verify firewall rules: `iptables -L -n`

### Connection Timeout
- Check network connectivity: `ping <vpp-host>`
- Verify port is open: `telnet <vpp-host> 5002`

### API Errors
- Ensure VPP API version compatibility
- Check VPP logs: `journalctl -u vpp -f`
