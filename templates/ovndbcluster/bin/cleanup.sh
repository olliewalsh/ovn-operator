#!/usr/bin/env bash
#
# Copyright 2022 Red Hat Inc.
#
# Licensed under the Apache License, Version 2.0 (the "License"); you may
# not use this file except in compliance with the License. You may obtain
# a copy of the License at
#
#      http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
# WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
# License for the specific language governing permissions and limitations
# under the License.
set -ex
source $(dirname $0)/functions

# There is nothing special about -0 pod, except that it's always guaranteed to
# exist, assuming any replicas are ordered.
if [[ "$(hostname)" != "{{ .SERVICE_NAME }}-0" ]]; then
    leave_cluster
fi

# If replicas are 0 and *all* pods are removed, we still want to retain the
# database with its cid/sid for when the cluster is scaled back to > 0, so
# leaving the database file intact for -0 pod.
if [[ "$(hostname)" != "{{ .SERVICE_NAME }}-0" ]]; then
    # now that we left, the database file is no longer valid
    cleanup_db_file
fi

# Stop the DB server gracefully; this will also end the pod running script.
# By executing the stop command in the background, we guarantee to exit
# this script and not fail with "FailedPreStopHook", while the database is
# stopped.
/usr/share/ovn/scripts/ovn-ctl stop_${DB_TYPE}_ovsdb &
