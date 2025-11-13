# WebSocket Support for Terminal Extension

This document describes how to use the WebSocket endpoint for command execution in the terminal extension.

## WebSocket Endpoint

The WebSocket endpoint is available at `/ws/exec`. This endpoint allows clients to execute shell commands and receive real-time output through a WebSocket connection.

## Message Format

Communication between client and server uses JSON formatted messages.

### Client to Server Messages

Clients send command requests using the following format:

```json
{
  "cmd": "ls -la",
  "terminalId": "unique-terminal-id",
  "terminalName": "My Terminal"
}
```

Where:
- `cmd`: The command to execute
- `terminalId`: Unique identifier for the terminal session
- `terminalName`: Human-readable name for the terminal

### Server to Client Messages

The server sends various types of messages during command execution:

#### Start Message
Sent when a command begins execution:
```json
{
  "type": "start"
}
```

#### PID Message
Sent with the process ID of the executing command:
```json
{
  "type": "pid",
  "pid": 12345
}
```

#### Stdout Message
Contains standard output from the command:
```json
{
  "type": "stdout",
  "data": "file1.txt file2.txt"
}
```

#### Stderr Message
Contains standard error output from the command:
```json
{
  "type": "stderr",
  "data": "Error: File not found"
}
```

#### End Message
Sent when the command completes execution:
```json
{
  "type": "end",
  "exitCode": 0
}
```

#### Error Message
Sent when an error occurs:
```json
{
  "type": "error",
  "error": "Error description"
}
```

## Example Usage

Here's a simple JavaScript example showing how to connect and execute commands:

```javascript
// Connect to WebSocket server
const ws = new WebSocket('ws://localhost:4076/ws/exec');

ws.onopen = function() {
    console.log('Connected to WebSocket server');
    
    // Send a command
    const command = {
        cmd: 'ls -la',
        terminalId: 'my-terminal',
        terminalName: 'My Terminal'
    };
    
    ws.send(JSON.stringify(command));
};

ws.onmessage = function(event) {
    const message = JSON.parse(event.data);
    
    switch(message.type) {
        case 'start':
            console.log('Command started');
            break;
        case 'stdout':
            console.log('Output:', message.data);
            break;
        case 'stderr':
            console.log('Error:', message.data);
            break;
        case 'end':
            console.log('Command finished with exit code:', message.exitCode);
            break;
        case 'error':
            console.log('Error occurred:', message.error);
            break;
    }
};
```

## Benefits of WebSocket Implementation

1. **Real-time Communication**: Bidirectional communication allows for real-time command execution and output streaming
2. **Lower Latency**: WebSocket connections have lower overhead compared to repeated HTTP requests
3. **Persistent Connection**: Maintains a continuous connection, eliminating the need for reconnection overhead
4. **Full Duplex**: Allows simultaneous sending and receiving of data

## Differences from Existing HTTP SSE Endpoint

While the existing HTTP SSE (`/extensionProxy/terminal`) endpoint also provides streaming capabilities, the WebSocket implementation offers:

1. **Bidirectional Communication**: Unlike SSE which is unidirectional, WebSocket allows sending input to running processes
2. **Better Connection Management**: More efficient connection handling for long-running interactive sessions
3. **Wider Browser Support**: WebSocket is supported by all modern browsers with consistent APIs
4. **Lower Overhead**: WebSocket frames have less overhead than HTTP headers for each message

Both endpoints can coexist, allowing developers to choose the best option for their specific use case.