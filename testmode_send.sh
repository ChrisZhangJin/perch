#!/bin/bash
#
curl -X POST http://127.0.0.1:9876/inject -H 'Content-Type: application/json' -d '{"from":"chris@wiz.ai","to":"agent@x","subject":"hi","body":"Hi support team,How are you? can you tell me if I am the member of the kulink service?Regards,Chris"}'
