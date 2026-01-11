# Problem 2: Means to an End

To keep bandwidth usage down, a simple binary format has been specified.

## Overview

Clients will connect to your server using TCP. Each client tracks the price of a different asset. Clients send messages to the server that either **insert** or **query** the prices.

Each connection from a client is a separate session. Each session's data represents a different asset, so each session can only query the data supplied by itself.

## Message format

Each message from a client is **9 bytes** long. Clients can send multiple messages per connection. Messages are not delimited by newlines or any other character: you'll know where one message ends and the next starts because they are always 9 bytes.

| Byte: | 0 | 1 2 3 4 | 5 6 7 8 |
| :--- | :--- | :--- | :--- |
| **Type:** | char | int32 | int32 |

The first byte of a message is a character indicating its type. This will be an ASCII uppercase **'I'** or **'Q'** character. The next 8 bytes are two signed two's-complement 32-bit integers in **network byte order** (big-endian).

### Insert (I)

An insert message looks like this:
* Type: **'I'** (ASCII 73)
* Timestamp: **int32**
* Price: **int32**

This inserts a price for the given timestamp.

### Query (Q)

A query message looks like this:
* Type: **'Q'** (ASCII 81)
* MinTime: **int32**
* MaxTime: **int32**

A query asks the server to compute the **mean (average) price** of the asset between the given timestamps (inclusive).

The server must return a single **4-byte signed 32-bit integer** (in network byte order) representing the mean price.

* If `MinTime` is greater than `MaxTime`, or if no prices have been inserted for the given range, the mean is **0**.
* The mean should be calculated using integer division (rounding towards zero). Note that while prices are typically positive, they can be negative.

## Specification

* **Accept TCP connections.**
* Handle multiple concurrent clients (at least 5).
* Messages are exactly 9 bytes; results are exactly 4 bytes.
* Insertions may occur out-of-order.
* If multiple prices are inserted for the same timestamp, behavior is undefined (you can assume this won't happen or handle it however you like).
* Your server should handle at least 100,000 insertions and queries per session without running out of memory.
