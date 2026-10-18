#!/usr/bin/python
# -*- coding: utf-8 -*-

# Copyright: (c) 2026, Onur Baydaş <onurbaydas1@gmail.com>
# GNU General Public License v3.0+ (see COPYING or https://www.gnu.org/licenses/gpl-3.0.txt)

from __future__ import absolute_import, division, print_function
__metaclass__ = type

DOCUMENTATION = r'''
---
module: virtomir_network
short_description: Manage virtual networks in VirtoMir
description:
  - Create, update, start, stop, or delete virtual networks (NAT, routed, isolated, bridged) in VirtoMir.
version_added: "0.7.0"
author:
  - Onur Baydaş (@onurbaydas)
options:
  url:
    description: Base URL of the VirtoMir daemon (e.g. https://192.168.77.10:8420).
    type: str
    required: true
  token:
    description: API token with Admin or Operator role.
    type: str
    required: true
    no_log: true
  validate_certs:
    description: Validate TLS certificates.
    type: bool
    default: true
  name:
    description: Network name.
    type: str
    required: true
  mode:
    description: Network forwarding mode.
    type: str
    choices: [nat, route, isolated, bridge]
    default: nat
  bridge:
    description: Host bridge interface to bind to (required for mode=bridge).
    type: str
  ipv4_subnet:
    description: IPv4 CIDR subnet (e.g. 192.168.100.0/24).
    type: str
  ipv4_dhcp_start:
    description: First address of DHCP range.
    type: str
  ipv4_dhcp_end:
    description: Last address of DHCP range.
    type: str
  ipv6_subnet:
    description: Optional IPv6 /64 CIDR.
    type: str
  autostart:
    description: Automatically start network with host.
    type: bool
    default: true
  state:
    description: Desired network state.
    type: str
    choices: [present, absent, active, inactive]
    default: present
'''

EXAMPLES = r'''
- name: Create NAT network with IPv4 and IPv6
  onurbaydas.virtomir.virtomir_network:
    url: https://virtomir.local:8420
    token: vmt_...
    validate_certs: false
    name: internal-net
    mode: nat
    ipv4_subnet: 192.168.150.0/24
    ipv4_dhcp_start: 192.168.150.100
    ipv4_dhcp_end: 192.168.150.200
    autostart: true
    state: present
'''

from ansible.module_utils.basic import AnsibleModule
from ansible.module_utils.urls import fetch_url
import json


def call_api(module, method, endpoint, data=None):
    base = module.params['url'].rstrip('/')
    url = "{0}/api/v1{1}".format(base, endpoint)
    headers = {
        'Authorization': 'Bearer ' + module.params['token'],
        'Content-Type': 'application/json',
    }
    payload = json.dumps(data) if data is not None else None
    resp, info = fetch_url(module, url, method=method, data=payload, headers=headers)
    status = info.get('status', -1)
    body = None
    if resp:
        try:
            body = json.loads(resp.read().decode('utf-8'))
        except Exception:
            body = None
    if status >= 400:
        msg = (body.get('error') if isinstance(body, dict) else None) or info.get('msg', 'API error')
        module.fail_json(msg="VirtoMir API {0} {1} failed ({2}): {3}".format(method, endpoint, status, msg))
    return body


def main():
    module = AnsibleModule(
        argument_spec=dict(
            url=dict(type='str', required=True),
            token=dict(type='str', required=True, no_log=True),
            validate_certs=dict(type='bool', default=True),
            name=dict(type='str', required=True),
            mode=dict(type='str', choices=['nat', 'route', 'isolated', 'bridge'], default='nat'),
            bridge=dict(type='str'),
            ipv4_subnet=dict(type='str'),
            ipv4_dhcp_start=dict(type='str'),
            ipv4_dhcp_end=dict(type='str'),
            ipv6_subnet=dict(type='str'),
            autostart=dict(type='bool', default=True),
            state=dict(type='str', choices=['present', 'absent', 'active', 'inactive'], default='present'),
        ),
        supports_check_mode=True,
    )

    name = module.params['name']
    state = module.params['state']

    networks = call_api(module, 'GET', '/networks') or []
    current = next((n for n in networks if n.get('name') == name), None)

    changed = False

    if state == 'absent':
        if current is not None:
            if not module.check_mode:
                call_api(module, 'DELETE', "/networks/{0}".format(name))
            changed = True
        module.exit_json(changed=changed)

    if current is None:
        if not module.check_mode:
            payload = {
                'name': name,
                'mode': module.params['mode'],
                'bridge': module.params.get('bridge'),
                'ipv4Subnet': module.params.get('ipv4_subnet'),
                'ipv4DhcpStart': module.params.get('ipv4_dhcp_start'),
                'ipv4DhcpEnd': module.params.get('ipv4_dhcp_end'),
                'ipv6Subnet': module.params.get('ipv6_subnet'),
                'autostart': module.params['autostart'],
            }
            call_api(module, 'POST', '/networks', payload)
        changed = True
    else:
        # Check active/inactive state
        is_active = current.get('active', False)
        if state == 'active' and not is_active:
            if not module.check_mode:
                call_api(module, 'POST', "/networks/{0}/start".format(name))
            changed = True
        elif state == 'inactive' and is_active:
            if not module.check_mode:
                call_api(module, 'POST', "/networks/{0}/stop".format(name))
            changed = True

    module.exit_json(changed=changed, network=current)


if __name__ == '__main__':
    main()
