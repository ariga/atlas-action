#!/bin/bash

if [ -n "$TEST_COUNTER_FILE" ]; then
    expr `cat $TEST_COUNTER_FILE 2>/dev/null` + 1 >$TEST_COUNTER_FILE
fi

echo "${TEST_STDOUT}"
exit ${TEST_EXIT_CODE}
