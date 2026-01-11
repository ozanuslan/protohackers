# Problem 6: Speed Daemon

Motorists on Freedom Island drive as fast as they like. Sadly, this has led to a large number of crashes, so the islanders have agreed to impose speed limits. The speed limits will be enforced via an average speed check: Automatic Number Plate Recognition cameras will be installed at various points on the road network.

## Overview

You need to build a server to coordinate enforcement of average speed limits on the Freedom Island road network. Your server will handle two types of client: **cameras** and **ticket dispatchers**.

Clients connect over TCP and speak a protocol using a binary format. Make sure you support at least **150 simultaneous clients**.

### Cameras
Each camera is on a specific road, at a specific location, and has a specific speed limit. Cameras report each number plate that they observe, along with the timestamp that they observed it. Timestamps are unsigned integers (Unix timestamps).

### Ticket dispatchers
Each ticket dispatcher is responsible for some number of roads. When the server finds that a car was detected at 2 points on the same road with an average speed in excess of the speed limit (`speed = distance / time`), it will find the responsible ticket dispatcher and send it a ticket.

### Roads and Cars
* **Roads:** Identified by a number from 0 to 65535. Positions are integer miles from the start of the road.
* **Cars:** Identified by an uppercase alphanumeric string (number plate).

## Data Types

All integers are transmitted in **network byte-order** (big-endian).

* `u8`, `u16`, `u32`: Unsigned integers of 8, 16, and 32 bits respectively.
* `str`: A length-prefixed string. A single `u8` contains the length (0-255), followed by that many bytes of ASCII character codes.

## Message Types

Each message starts with a single `u8` specifying the message type. Messages are concatenated with no padding.

### 0x10: Error (Server -> Client)
* `msg`: `str`
Sent when a client violates the protocol. The server must disconnect the client immediately after sending this.

### 0x20: Plate (Client -> Server)
* `plate`: `str`
* `timestamp`: `u32`
Observed plate at the camera's location and timestamp. Observations can arrive in any order. It is an error for a non-camera client to send this.

### 0x21: Ticket (Server -> Client)
* `plate`: `str`
* `road`: `u16`
* `mile1`: `u16`
* `timestamp1`: `u32`
* `mile2`: `u16`
* `timestamp2`: `u32`
* `speed`: `u16` (100x miles per hour)
Sent to a dispatcher. `mile1`/`timestamp1` must be the earlier observation; `mile2`/`timestamp2` the later.

### 0x40: WantHeartbeat (Client -> Server)
* `interval`: `u32` (deciseconds)
Request the server send `0x41 Heartbeat` messages every `interval/10` seconds. `0` means no heartbeats. It is an error to send this more than once per connection.

### 0x41: Heartbeat (Server -> Client)
No fields. Sent at the requested interval.

### 0x80: IAmCamera (Client -> Server)
* `road`: `u16`
* `mile`: `u16`
* `limit`: `u16` (mph)
Identifies the client as a camera. It is an error to send this if already identified.

### 0x81: IAmDispatcher (Client -> Server)
* `numroads`: `u8`
* `roads`: `[u16]` (array of `u16`)
Identifies the client as a dispatcher for the given roads. It is an error to send this if already identified.

## Details

### Dispatchers
* If multiple dispatchers cover a road, pick one arbitrarily. Never send the same ticket twice.
* If no dispatcher is available, store the ticket and deliver it once one becomes available.
* If a dispatcher disconnects during delivery, the ticket is lost.

### Rules and Limitations
* **Unreliable cameras:** Cars may skip cameras. Check average speed between **any** pair of observations on the same road.
* **Only 1 ticket per car per day:** A car may receive at most one ticket per day. Days are defined as `floor(timestamp / 86400)`. If a ticket spans multiple days, it counts for every day in that range.
* **Rounding:** Ticket if the speed exceeds the limit by 0.5 mph or more. Never ticket if below the limit.
* **Overflow:** Assume no car exceeds 655.35 mph.
