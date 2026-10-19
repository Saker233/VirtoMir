# -*- coding: utf-8 -*-
# Copyright: (c) 2026, Onur Baydas
# GNU General Public License v3.0+ (see LICENSE or https://www.gnu.org/licenses/gpl-3.0.txt)

import os
import sys
import types
import unittest
import urllib.error
import urllib.parse
from unittest.mock import MagicMock, patch

# --- Setup import paths and mock ansible shims if ansible is not installed ---
CUR_DIR = os.path.dirname(os.path.abspath(__file__))
ANSIBLE_DIR = os.path.abspath(os.path.join(CUR_DIR, '..', '..'))
PLUGINS_DIR = os.path.join(ANSIBLE_DIR, 'plugins')
MODULES_DIR = os.path.join(PLUGINS_DIR, 'modules')
MODULE_UTILS_DIR = os.path.join(PLUGINS_DIR, 'module_utils')

for p in [ANSIBLE_DIR, PLUGINS_DIR, MODULES_DIR, MODULE_UTILS_DIR]:
    if p not in sys.path:
        sys.path.insert(0, p)


class AnsibleExitJson(Exception):
    def __init__(self, **kwargs):
        self.kwargs = kwargs
        super(AnsibleExitJson, self).__init__(str(kwargs))


class AnsibleFailJson(Exception):
    def __init__(self, **kwargs):
        self.kwargs = kwargs
        super(AnsibleFailJson, self).__init__(str(kwargs))


def raise_exit(**kwargs):
    raise AnsibleExitJson(**kwargs)


def raise_fail(**kwargs):
    raise AnsibleFailJson(**kwargs)


class MockAnsibleModule(object):
    def __init__(self, argument_spec, supports_check_mode=False, mutually_exclusive=None):
        self.argument_spec = argument_spec
        self.supports_check_mode = supports_check_mode
        self.check_mode = False
        self.params = {}
        for k, v in argument_spec.items():
            if 'default' in v:
                self.params[k] = v['default']
            else:
                self.params[k] = None

    def exit_json(self, **kwargs):
        raise AnsibleExitJson(**kwargs)

    def fail_json(self, **kwargs):
        raise AnsibleFailJson(**kwargs)


def _ensure_module(mod_name, path=None):
    if mod_name not in sys.modules:
        m = types.ModuleType(mod_name)
        if path is not None:
            m.__path__ = [path] if isinstance(path, str) else list(path)
            m.__package__ = mod_name
        sys.modules[mod_name] = m
    return sys.modules[mod_name]


# Ensure ansible and ansible.module_utils shims
ansible_mod = _ensure_module('ansible', ANSIBLE_DIR)
ansible_mu = _ensure_module('ansible.module_utils', MODULE_UTILS_DIR)
ansible_basic = _ensure_module('ansible.module_utils.basic')
if not hasattr(ansible_basic, 'AnsibleModule'):
    ansible_basic.AnsibleModule = MockAnsibleModule
if not hasattr(ansible_basic, 'env_fallback'):
    ansible_basic.env_fallback = lambda *args: None

ansible_urls = _ensure_module('ansible.module_utils.urls')
if not hasattr(ansible_urls, 'open_url'):
    ansible_urls.open_url = MagicMock()

ansible_six = _ensure_module('ansible.module_utils.six')
ansible_six_moves = _ensure_module('ansible.module_utils.six.moves')
ansible_six_urllib = _ensure_module('ansible.module_utils.six.moves.urllib')
ansible_six_urllib_error = _ensure_module('ansible.module_utils.six.moves.urllib.error')
ansible_six_urllib_parse = _ensure_module('ansible.module_utils.six.moves.urllib.parse')

ansible_six_urllib_error.HTTPError = urllib.error.HTTPError
ansible_six_urllib_parse.quote = urllib.parse.quote

# Import virtomir client
import virtomir as virtomir_util

# Register ansible_collections hierarchy
_ensure_module('ansible_collections', ANSIBLE_DIR)
_ensure_module('ansible_collections.onurbaydas', ANSIBLE_DIR)
_ensure_module('ansible_collections.onurbaydas.virtomir', ANSIBLE_DIR)
_ensure_module('ansible_collections.onurbaydas.virtomir.plugins', PLUGINS_DIR)

col_mu = _ensure_module('ansible_collections.onurbaydas.virtomir.plugins.module_utils', MODULE_UTILS_DIR)
col_virtomir = _ensure_module('ansible_collections.onurbaydas.virtomir.plugins.module_utils.virtomir')
col_virtomir.APIError = virtomir_util.APIError
col_virtomir.Client = virtomir_util.Client
col_virtomir.common_args = virtomir_util.common_args
col_virtomir.is_running = virtomir_util.is_running

col_mod = _ensure_module('ansible_collections.onurbaydas.virtomir.plugins.modules', MODULES_DIR)

import machine
import virtomir_machine
import virtomir_network
import virtomir_pool
import virtomir_project
import virtomir_placement_rule
import virtomir_snapshot
import network
import pool
import project
import placement_rule
import snapshot

col_mod.machine = machine
col_mod.virtomir_machine = virtomir_machine
col_mod.virtomir_network = virtomir_network
col_mod.virtomir_pool = virtomir_pool
col_mod.virtomir_project = virtomir_project
col_mod.virtomir_placement_rule = virtomir_placement_rule
col_mod.virtomir_snapshot = virtomir_snapshot
col_mod.network = network
col_mod.pool = pool
col_mod.project = project
col_mod.placement_rule = placement_rule
col_mod.snapshot = snapshot


def setup_mock_module(mock_ansible, params, check_mode=False):
    mock_mod = MagicMock()
    mock_mod.check_mode = check_mode
    mock_mod.params = dict(params)
    mock_mod.exit_json.side_effect = raise_exit
    mock_mod.fail_json.side_effect = raise_fail
    mock_ansible.return_value = mock_mod
    return mock_mod


class TestVirtomirClientHelpers(unittest.TestCase):
    def setUp(self):
        mod = MockAnsibleModule({})
        mod.params = {'url': 'http://fake-host:8420', 'token': 'fake-token'}
        self.client = virtomir_util.Client(mod)

    @patch.object(virtomir_util.Client, 'call')
    def test_networks_and_pool_helpers(self, mock_call):
        # Networks
        mock_call.return_value = {'networks': [{'name': 'net1', 'uuid': 'u-net1'}]}
        nets = self.client.networks()
        self.assertEqual(len(nets), 1)
        self.assertEqual(nets[0]['name'], 'net1')

        self.assertEqual(self.client.network('net1')['uuid'], 'u-net1')
        self.assertIsNone(self.client.network('nonexistent'))

        self.client.create_network({'name': 'net2'})
        mock_call.assert_called_with('POST', '/api/v1/networks', {'name': 'net2'})

        self.client.delete_network('net1')
        mock_call.assert_called_with('DELETE', '/api/v1/networks/net1')

        # Storage pools
        mock_call.return_value = {'pools': [{'name': 'pool1', 'uuid': 'u-pool1'}]}
        pools = self.client.pools()
        self.assertEqual(len(pools), 1)
        self.assertEqual(pools[0]['name'], 'pool1')

        self.assertEqual(self.client.pool('pool1')['uuid'], 'u-pool1')
        self.assertIsNone(self.client.pool('nonexistent'))

        self.client.create_pool({'name': 'pool2', 'type': 'dir'})
        mock_call.assert_called_with('POST', '/api/v1/storage/pools', {'name': 'pool2', 'type': 'dir'})

        self.client.delete_pool('pool1')
        mock_call.assert_called_with('DELETE', '/api/v1/storage/pools/pool1')

    @patch.object(virtomir_util.Client, 'call')
    def test_project_helpers(self, mock_call):
        mock_call.return_value = {'projects': [{'id': 'proj-1', 'name': 'Project 1'}]}
        projs = self.client.projects()
        self.assertEqual(len(projs), 1)

        self.assertEqual(self.client.project('proj-1')['name'], 'Project 1')
        self.assertEqual(self.client.project('Project 1')['id'], 'proj-1')
        self.assertIsNone(self.client.project('nope'))

        mock_call.return_value = {'project': {'id': 'proj-2', 'name': 'Project 2'}}
        created = self.client.create_project({'id': 'proj-2', 'name': 'Project 2'})
        self.assertEqual(created['id'], 'proj-2')

        updated = self.client.update_project('proj-2', {'name': 'Project 2 Modified'})
        self.assertEqual(updated['id'], 'proj-2')

        self.client.delete_project('proj-2')
        mock_call.assert_called_with('DELETE', '/api/v1/projects/proj-2')

    @patch.object(virtomir_util.Client, 'call')
    def test_placement_rule_helpers(self, mock_call):
        mock_call.return_value = {'rules': [{'id': 'rule-1', 'name': 'Rule 1', 'kind': 'vm_affinity'}]}
        rules = self.client.placement_rules()
        self.assertEqual(len(rules), 1)

        self.assertEqual(self.client.placement_rule('rule-1')['name'], 'Rule 1')
        self.assertEqual(self.client.placement_rule('Rule 1')['id'], 'rule-1')
        self.assertIsNone(self.client.placement_rule('nope'))

        self.client.create_placement_rule({'name': 'Rule 2', 'kind': 'vm_anti_affinity'})
        mock_call.assert_called_with('POST', '/api/v1/placement/rules', {'name': 'Rule 2', 'kind': 'vm_anti_affinity'})

        self.client.update_placement_rule('rule-1', {'name': 'Rule 1 Renamed'})
        mock_call.assert_called_with('PUT', '/api/v1/placement/rules/rule-1', {'name': 'Rule 1 Renamed'})

        self.client.delete_placement_rule('rule-1')
        mock_call.assert_called_with('DELETE', '/api/v1/placement/rules/rule-1')

    @patch.object(virtomir_util.Client, 'call')
    def test_snapshot_helpers(self, mock_call):
        mock_call.return_value = {'snapshots': [{'name': 'snap1', 'current': True}]}
        snaps = self.client.snapshots('vm-uuid-1')
        self.assertEqual(len(snaps), 1)

        self.assertEqual(self.client.snapshot('vm-uuid-1', 'snap1')['name'], 'snap1')
        self.assertIsNone(self.client.snapshot('vm-uuid-1', 'nope'))

        self.client.create_snapshot('vm-uuid-1', {'name': 'snap2'})
        mock_call.assert_called_with('POST', '/api/v1/domains/vm-uuid-1/snapshots', {'name': 'snap2'})

        self.client.revert_snapshot('vm-uuid-1', 'snap1')
        mock_call.assert_called_with('POST', '/api/v1/domains/vm-uuid-1/snapshots/snap1/revert')

        self.client.delete_snapshot('vm-uuid-1', 'snap1')
        mock_call.assert_called_with('DELETE', '/api/v1/domains/vm-uuid-1/snapshots/snap1')


class TestNetworkModule(unittest.TestCase):
    @patch('virtomir_network.Client')
    @patch('virtomir_network.AnsibleModule')
    def test_network_check_mode_create(self, mock_ansible, mock_client_cls):
        setup_mock_module(mock_ansible, {
            'name': 'vnet-new',
            'mode': 'nat',
            'bridge': None,
            'ipv4_subnet': '192.168.200.0/24',
            'ipv4_dhcp_start': None,
            'ipv4_dhcp_end': None,
            'ipv6_subnet': None,
            'autostart': True,
            'state': 'present',
        }, check_mode=True)
        mock_client = MagicMock()
        mock_client.network.return_value = None
        mock_client_cls.return_value = mock_client

        with self.assertRaises(AnsibleExitJson) as ctx:
            virtomir_network.main()

        res = ctx.exception.kwargs
        self.assertTrue(res['changed'])
        self.assertIsNone(res['network'])
        mock_client.create_network.assert_not_called()

    @patch('virtomir_network.Client')
    @patch('virtomir_network.AnsibleModule')
    def test_network_create_real(self, mock_ansible, mock_client_cls):
        setup_mock_module(mock_ansible, {
            'name': 'vnet-new',
            'mode': 'nat',
            'bridge': None,
            'ipv4_subnet': '192.168.200.0/24',
            'ipv4_dhcp_start': None,
            'ipv4_dhcp_end': None,
            'ipv6_subnet': None,
            'autostart': True,
            'state': 'present',
        }, check_mode=False)
        mock_client = MagicMock()
        mock_client.network.side_effect = [None, {'name': 'vnet-new', 'mode': 'nat', 'active': True}]
        mock_client_cls.return_value = mock_client

        with self.assertRaises(AnsibleExitJson) as ctx:
            virtomir_network.main()

        res = ctx.exception.kwargs
        self.assertTrue(res['changed'])
        mock_client.create_network.assert_called_once()

    @patch('virtomir_network.Client')
    @patch('virtomir_network.AnsibleModule')
    def test_network_create_custom_dhcp_and_ipv6(self, mock_ansible, mock_client_cls):
        # 1. Custom IPv4 DHCP range
        setup_mock_module(mock_ansible, {
            'name': 'vnet-custom-dhcp',
            'mode': 'nat',
            'bridge': None,
            'dhcp': True,
            'ipv4_subnet': '192.168.100.0/24',
            'ipv4_dhcp_start': '192.168.100.50',
            'ipv4_dhcp_end': '192.168.100.150',
            'ipv6_subnet': 'fd12:3456:789a::/64',
            'dhcp6': True,
            'ipv6_dhcp_start': 'fd12:3456:789a::100',
            'ipv6_dhcp_end': 'fd12:3456:789a::200',
            'autostart': True,
            'state': 'present',
        }, check_mode=False)
        mock_client = MagicMock()
        mock_client.network.side_effect = [None, {'name': 'vnet-custom-dhcp', 'active': True}]
        mock_client_cls.return_value = mock_client

        with self.assertRaises(AnsibleExitJson):
            virtomir_network.main()

        mock_client.create_network.assert_called_once_with({
            'name': 'vnet-custom-dhcp',
            'mode': 'nat',
            'subnet': '192.168.100.0/24',
            'dhcp': True,
            'dhcpStart': '192.168.100.50',
            'dhcpEnd': '192.168.100.150',
            'subnet6': 'fd12:3456:789a::/64',
            'dhcp6': True,
            'dhcp6Start': 'fd12:3456:789a::100',
            'dhcp6End': 'fd12:3456:789a::200',
        })

    @patch('virtomir_network.Client')
    @patch('virtomir_network.AnsibleModule')
    def test_network_create_dhcp_disabled_and_ipv6_slaac_only(self, mock_ansible, mock_client_cls):
        # 2. DHCP disabled for IPv4 and SLAAC only for IPv6 (dhcp6=False)
        setup_mock_module(mock_ansible, {
            'name': 'vnet-static',
            'mode': 'isolated',
            'bridge': None,
            'dhcp': False,
            'ipv4_subnet': '10.10.0.0/16',
            'ipv4_dhcp_start': None,
            'ipv4_dhcp_end': None,
            'ipv6_subnet': 'fd00:10::/64',
            'dhcp6': False,
            'ipv6_dhcp_start': None,
            'ipv6_dhcp_end': None,
            'autostart': True,
            'state': 'present',
        }, check_mode=False)
        mock_client = MagicMock()
        mock_client.network.side_effect = [None, {'name': 'vnet-static', 'active': True}]
        mock_client_cls.return_value = mock_client

        with self.assertRaises(AnsibleExitJson):
            virtomir_network.main()

        mock_client.create_network.assert_called_once_with({
            'name': 'vnet-static',
            'mode': 'isolated',
            'subnet': '10.10.0.0/16',
            'dhcp': False,
            'subnet6': 'fd00:10::/64',
        })

    @patch('virtomir_network.Client')
    @patch('virtomir_network.AnsibleModule')
    def test_network_idempotent_present(self, mock_ansible, mock_client_cls):
        setup_mock_module(mock_ansible, {
            'name': 'vnet-existing',
            'mode': 'nat',
            'bridge': None,
            'ipv4_subnet': '192.168.100.0/24',
            'ipv4_dhcp_start': None,
            'ipv4_dhcp_end': None,
            'ipv6_subnet': None,
            'autostart': True,
            'state': 'present',
        }, check_mode=False)
        mock_client = MagicMock()
        mock_client.network.return_value = {'name': 'vnet-existing', 'mode': 'nat', 'autostart': True, 'active': True}
        mock_client_cls.return_value = mock_client

        with self.assertRaises(AnsibleExitJson) as ctx:
            virtomir_network.main()

        res = ctx.exception.kwargs
        self.assertFalse(res['changed'])
        mock_client.create_network.assert_not_called()
        mock_client.set_network_autostart.assert_not_called()

    @patch('virtomir_network.Client')
    @patch('virtomir_network.AnsibleModule')
    def test_network_state_active_and_delete(self, mock_ansible, mock_client_cls):
        # 1. State active on inactive network
        mock_mod = setup_mock_module(mock_ansible, {
            'name': 'vnet-down',
            'mode': 'nat',
            'bridge': None,
            'ipv4_subnet': None,
            'ipv4_dhcp_start': None,
            'ipv4_dhcp_end': None,
            'ipv6_subnet': None,
            'autostart': True,
            'state': 'active',
        }, check_mode=False)
        mock_client = MagicMock()
        mock_client.network.return_value = {'name': 'vnet-down', 'autostart': True, 'active': False}
        mock_client_cls.return_value = mock_client

        with self.assertRaises(AnsibleExitJson) as ctx:
            virtomir_network.main()

        self.assertTrue(ctx.exception.kwargs['changed'])
        mock_client.start_network.assert_called_once_with('vnet-down')

        # 2. State absent on existing network
        mock_mod.params['state'] = 'absent'
        mock_client.network.return_value = {'name': 'vnet-down', 'active': False}

        with self.assertRaises(AnsibleExitJson) as ctx:
            virtomir_network.main()

        self.assertTrue(ctx.exception.kwargs['changed'])
        mock_client.delete_network.assert_called_once_with('vnet-down')

    @patch('network.main')
    def test_network_alias_module(self, mock_main):
        network.main()
        mock_main.assert_called_once()


class TestPoolModule(unittest.TestCase):
    @patch('virtomir_pool.Client')
    @patch('virtomir_pool.AnsibleModule')
    def test_pool_check_mode_and_create(self, mock_ansible, mock_client_cls):
        mock_mod = setup_mock_module(mock_ansible, {
            'name': 'fast-ssd',
            'type': 'dir',
            'path': '/srv/ssd',
            'source': None,
            'host': None,
            'export': None,
            'target': None,
            'devices': None,
            'autostart': True,
            'state': 'present',
        }, check_mode=True)
        mock_client = MagicMock()
        mock_client.pool.return_value = None
        mock_client_cls.return_value = mock_client

        with self.assertRaises(AnsibleExitJson) as ctx:
            virtomir_pool.main()

        self.assertTrue(ctx.exception.kwargs['changed'])
        self.assertIsNone(ctx.exception.kwargs['pool'])
        mock_client.create_pool.assert_not_called()

        # Real create
        mock_mod.check_mode = False
        mock_client.pool.side_effect = [None, {'name': 'fast-ssd', 'type': 'dir', 'autostart': True}]
        with self.assertRaises(AnsibleExitJson) as ctx:
            virtomir_pool.main()
        self.assertTrue(ctx.exception.kwargs['changed'])
        mock_client.create_pool.assert_called_once_with({'name': 'fast-ssd', 'type': 'dir', 'path': '/srv/ssd'})

    @patch('virtomir_pool.Client')
    @patch('virtomir_pool.AnsibleModule')
    def test_pool_idempotent_and_delete(self, mock_ansible, mock_client_cls):
        mock_mod = setup_mock_module(mock_ansible, {
            'name': 'fast-ssd',
            'type': 'dir',
            'path': '/srv/ssd',
            'source': None,
            'host': None,
            'export': None,
            'target': None,
            'devices': None,
            'autostart': True,
            'state': 'present',
        }, check_mode=False)
        mock_client = MagicMock()
        mock_client.pool.return_value = {'name': 'fast-ssd', 'type': 'dir', 'autostart': True, 'active': True}
        mock_client_cls.return_value = mock_client

        with self.assertRaises(AnsibleExitJson) as ctx:
            virtomir_pool.main()
        self.assertFalse(ctx.exception.kwargs['changed'])
        mock_client.create_pool.assert_not_called()

        # Delete
        mock_mod.params['state'] = 'absent'
        with self.assertRaises(AnsibleExitJson) as ctx:
            virtomir_pool.main()
        self.assertTrue(ctx.exception.kwargs['changed'])
        mock_client.delete_pool.assert_called_once_with('fast-ssd')

    @patch('pool.main')
    def test_pool_alias_module(self, mock_main):
        pool.main()
        mock_main.assert_called_once()


class TestProjectModule(unittest.TestCase):
    @patch('virtomir_project.Client')
    @patch('virtomir_project.AnsibleModule')
    def test_project_check_mode_and_create(self, mock_ansible, mock_client_cls):
        mock_mod = setup_mock_module(mock_ansible, {
            'id': 'dev-team',
            'name': 'Dev Team',
            'description': 'Dev sandbox',
            'members': [{'kind': 'user', 'name': 'alice', 'role': 'admin'}],
            'limits': {'machines': 5, 'vcpus': 20, 'memory_mib': 32768, 'disk_gib': 200},
            'storage_pools': ['default'],
            'networks': ['default'],
            'state': 'present',
        }, check_mode=True)
        mock_client = MagicMock()
        mock_client.project.return_value = None
        mock_client_cls.return_value = mock_client

        with self.assertRaises(AnsibleExitJson) as ctx:
            virtomir_project.main()

        self.assertTrue(ctx.exception.kwargs['changed'])
        self.assertIsNone(ctx.exception.kwargs['project'])
        mock_client.create_project.assert_not_called()

        # Real create
        mock_mod.check_mode = False
        mock_client.create_project.return_value = {'id': 'dev-team', 'name': 'Dev Team'}
        with self.assertRaises(AnsibleExitJson) as ctx:
            virtomir_project.main()
        self.assertTrue(ctx.exception.kwargs['changed'])
        mock_client.create_project.assert_called_once()

    @patch('virtomir_project.Client')
    @patch('virtomir_project.AnsibleModule')
    def test_project_idempotent_and_update(self, mock_ansible, mock_client_cls):
        mock_mod = setup_mock_module(mock_ansible, {
            'id': 'dev-team',
            'name': 'Dev Team',
            'description': 'Dev sandbox',
            'members': [{'kind': 'user', 'name': 'alice', 'role': 'admin'}],
            'limits': {'machines': 5, 'vcpus': 20, 'memory_mib': 32768, 'disk_gib': 200},
            'storage_pools': ['default'],
            'networks': ['default'],
            'state': 'present',
        }, check_mode=False)
        mock_client = MagicMock()
        existing_proj = {
            'id': 'dev-team',
            'name': 'Dev Team',
            'description': 'Dev sandbox',
            'members': [{'kind': 'user', 'name': 'alice', 'role': 'admin'}],
            'limits': {'machines': 5, 'vcpus': 20, 'memoryMiB': 32768, 'diskGiB': 200},
            'storagePools': ['default'],
            'networks': ['default'],
        }
        mock_client.project.return_value = existing_proj
        mock_client_cls.return_value = mock_client

        # 1. Matching config -> changed = False, no update
        with self.assertRaises(AnsibleExitJson) as ctx:
            virtomir_project.main()
        self.assertFalse(ctx.exception.kwargs['changed'])
        mock_client.update_project.assert_not_called()

        # 2. Modify limits -> changed = True, update called
        mock_mod.params['limits']['machines'] = 10
        with self.assertRaises(AnsibleExitJson) as ctx:
            virtomir_project.main()
        self.assertTrue(ctx.exception.kwargs['changed'])
        mock_client.update_project.assert_called_once()

    @patch('project.main')
    def test_project_alias_module(self, mock_main):
        project.main()
        mock_main.assert_called_once()


class TestPlacementRuleModule(unittest.TestCase):
    @patch('virtomir_placement_rule.Client')
    @patch('virtomir_placement_rule.AnsibleModule')
    def test_placement_rule_create_with_domain_resolution(self, mock_ansible, mock_client_cls):
        setup_mock_module(mock_ansible, {
            'name': 'db-anti-affinity',
            'kind': 'vm_anti_affinity',
            'strength': 'must',
            'domains': ['db-1', 'db-2'],
            'hosts': [],
            'exclude': False,
            'enabled': True,
            'state': 'present',
        }, check_mode=False)
        mock_client = MagicMock()
        mock_client.placement_rule.return_value = None
        mock_client.domain.side_effect = lambda name: {'db-1': {'uuid': 'u-db-1'}, 'db-2': {'uuid': 'u-db-2'}}.get(name)
        mock_client_cls.return_value = mock_client

        with self.assertRaises(AnsibleExitJson) as ctx:
            virtomir_placement_rule.main()

        self.assertTrue(ctx.exception.kwargs['changed'])
        mock_client.create_placement_rule.assert_called_once_with({
            'name': 'db-anti-affinity',
            'kind': 'vm_anti_affinity',
            'strength': 'must',
            'domains': ['u-db-1', 'u-db-2'],
            'enabled': True,
        })

    @patch('virtomir_placement_rule.Client')
    @patch('virtomir_placement_rule.AnsibleModule')
    def test_placement_rule_idempotency_and_update(self, mock_ansible, mock_client_cls):
        mock_mod = setup_mock_module(mock_ansible, {
            'name': 'db-anti-affinity',
            'kind': 'vm_anti_affinity',
            'strength': 'must',
            'domains': ['u-db-1', 'u-db-2'],
            'hosts': [],
            'exclude': False,
            'enabled': True,
            'state': 'present',
        }, check_mode=False)
        mock_client = MagicMock()
        mock_client.domain.return_value = None
        existing_rule = {
            'id': 'rule-123',
            'name': 'db-anti-affinity',
            'kind': 'vm_anti_affinity',
            'strength': 'must',
            'domains': ['u-db-1', 'u-db-2'],
            'hosts': [],
            'exclude': False,
            'enabled': True,
        }
        mock_client.placement_rule.return_value = existing_rule
        mock_client_cls.return_value = mock_client

        # Matching -> no update
        with self.assertRaises(AnsibleExitJson) as ctx:
            virtomir_placement_rule.main()
        self.assertFalse(ctx.exception.kwargs['changed'])
        mock_client.update_placement_rule.assert_not_called()

        # Change strength -> update called
        mock_mod.params['strength'] = 'should'
        with self.assertRaises(AnsibleExitJson) as ctx:
            virtomir_placement_rule.main()
        self.assertTrue(ctx.exception.kwargs['changed'])
        mock_client.update_placement_rule.assert_called_once()

    @patch('placement_rule.main')
    def test_placement_rule_alias_module(self, mock_main):
        placement_rule.main()
        mock_main.assert_called_once()


class TestSnapshotModule(unittest.TestCase):
    @patch('virtomir_snapshot.Client')
    @patch('virtomir_snapshot.AnsibleModule')
    def test_snapshot_create_check_mode_and_real(self, mock_ansible, mock_client_cls):
        mock_mod = setup_mock_module(mock_ansible, {
            'machine': 'web-1',
            'name': 'snap-initial',
            'description': 'Before patch',
            'memory': False,
            'state': 'present',
        }, check_mode=True)
        mock_client = MagicMock()
        mock_client.domain.return_value = {'name': 'web-1', 'uuid': 'u-web-1'}
        mock_client.snapshot.return_value = None
        mock_client_cls.return_value = mock_client

        with self.assertRaises(AnsibleExitJson) as ctx:
            virtomir_snapshot.main()

        self.assertTrue(ctx.exception.kwargs['changed'])
        mock_client.create_snapshot.assert_not_called()

        # Real create
        mock_mod.check_mode = False
        mock_client.snapshot.side_effect = [None, {'name': 'snap-initial', 'current': True}]
        with self.assertRaises(AnsibleExitJson) as ctx:
            virtomir_snapshot.main()
        self.assertTrue(ctx.exception.kwargs['changed'])
        mock_client.create_snapshot.assert_called_once_with('u-web-1', {'name': 'snap-initial', 'description': 'Before patch'})

    @patch('virtomir_snapshot.Client')
    @patch('virtomir_snapshot.AnsibleModule')
    def test_snapshot_idempotency_and_revert(self, mock_ansible, mock_client_cls):
        mock_mod = setup_mock_module(mock_ansible, {
            'machine': 'web-1',
            'name': 'snap-initial',
            'description': 'Before patch',
            'memory': False,
            'state': 'present',
        }, check_mode=False)
        mock_client = MagicMock()
        mock_client.domain.return_value = {'name': 'web-1', 'uuid': 'u-web-1'}
        mock_client.snapshot.return_value = {'name': 'snap-initial', 'current': False}
        mock_client_cls.return_value = mock_client

        # Existing snapshot with state=present is no-op
        with self.assertRaises(AnsibleExitJson) as ctx:
            virtomir_snapshot.main()
        self.assertFalse(ctx.exception.kwargs['changed'])
        mock_client.create_snapshot.assert_not_called()

        # Revert when current=False -> calls revert_snapshot
        mock_mod.params['state'] = 'reverted'
        with self.assertRaises(AnsibleExitJson) as ctx:
            virtomir_snapshot.main()
        self.assertTrue(ctx.exception.kwargs['changed'])
        mock_client.revert_snapshot.assert_called_once_with('u-web-1', 'snap-initial')

        # Revert when current=True -> no-op
        mock_client.revert_snapshot.reset_mock()
        mock_client.snapshot.return_value = {'name': 'snap-initial', 'current': True}
        with self.assertRaises(AnsibleExitJson) as ctx:
            virtomir_snapshot.main()
        self.assertFalse(ctx.exception.kwargs['changed'])
        mock_client.revert_snapshot.assert_not_called()

    @patch('snapshot.main')
    def test_snapshot_alias_module(self, mock_main):
        snapshot.main()
        mock_main.assert_called_once()


class TestMachineModule(unittest.TestCase):
    @patch('machine.Client')
    @patch('machine.AnsibleModule')
    def test_machine_check_mode_create(self, mock_ansible, mock_client_cls):
        setup_mock_module(mock_ansible, {
            'name': 'vm-new',
            'state': 'present',
            'os': 'linux',
            'vcpus': 2,
            'memory_mb': 2048,
            'autostart': False,
            'delete_disks': True,
            'force': False,
            'linked': False,
        }, check_mode=True)
        mock_client = MagicMock()
        mock_client.domain.return_value = None
        mock_client_cls.return_value = mock_client

        with self.assertRaises(AnsibleExitJson) as ctx:
            machine.main()

        res = ctx.exception.kwargs
        self.assertTrue(res['changed'])
        self.assertEqual(res['what'], ['created'])
        mock_client.call.assert_not_called()

    @patch('machine.Client')
    @patch('machine.AnsibleModule')
    def test_machine_create_real(self, mock_ansible, mock_client_cls):
        setup_mock_module(mock_ansible, {
            'name': 'vm-new',
            'state': 'present',
            'os': 'linux',
            'vcpus': 2,
            'memory_mb': 2048,
            'autostart': False,
            'delete_disks': True,
            'force': False,
            'linked': False,
        }, check_mode=False)
        mock_client = MagicMock()
        mock_client.domain.side_effect = [None, {'name': 'vm-new', 'uuid': 'u-vm-1', 'state': 'shutoff'}]
        mock_client.call.return_value = {'uuid': 'u-vm-1'}
        mock_client_cls.return_value = mock_client

        with self.assertRaises(AnsibleExitJson) as ctx:
            machine.main()

        res = ctx.exception.kwargs
        self.assertTrue(res['changed'])
        self.assertEqual(res['what'], ['created'])
        self.assertEqual(res['machine']['uuid'], 'u-vm-1')
        mock_client.call.assert_called_once()

    @patch('machine.Client')
    @patch('machine.AnsibleModule')
    def test_machine_idempotent_present(self, mock_ansible, mock_client_cls):
        setup_mock_module(mock_ansible, {
            'name': 'vm-existing',
            'state': 'present',
            'os': 'linux',
            'vcpus': 2,
            'memory_mb': 2048,
            'autostart': False,
            'delete_disks': True,
            'force': False,
            'linked': False,
        }, check_mode=False)
        mock_client = MagicMock()
        mock_client.domain.return_value = {
            'name': 'vm-existing',
            'uuid': 'u-vm-existing',
            'state': 'shutoff',
            'vcpus': 2,
            'maxMemoryKiB': 2048 << 10,
            'autostart': False,
        }
        mock_client_cls.return_value = mock_client

        with self.assertRaises(AnsibleExitJson) as ctx:
            machine.main()

        res = ctx.exception.kwargs
        self.assertFalse(res['changed'])
        self.assertEqual(res['what'], [])
        mock_client.call.assert_not_called()

    @patch('machine.Client')
    @patch('machine.AnsibleModule')
    def test_machine_state_running_and_absent(self, mock_ansible, mock_client_cls):
        mock_mod = setup_mock_module(mock_ansible, {
            'name': 'vm-existing',
            'state': 'running',
            'os': 'linux',
            'vcpus': 2,
            'memory_mb': 2048,
            'autostart': False,
            'delete_disks': True,
            'force': False,
            'linked': False,
        }, check_mode=False)
        mock_client = MagicMock()
        mock_client.domain.return_value = {
            'name': 'vm-existing',
            'uuid': 'u-vm-existing',
            'state': 'shutoff',
            'vcpus': 2,
            'maxMemoryKiB': 2048 << 10,
            'autostart': False,
        }
        mock_client_cls.return_value = mock_client

        with self.assertRaises(AnsibleExitJson) as ctx:
            machine.main()

        res = ctx.exception.kwargs
        self.assertTrue(res['changed'])
        mock_client.action.assert_called_once_with('u-vm-existing', 'start')

        # Absent
        mock_mod.params['state'] = 'absent'
        with self.assertRaises(AnsibleExitJson) as ctx:
            machine.main()
        self.assertTrue(ctx.exception.kwargs['changed'])
        mock_client.stop.assert_called_once_with('u-vm-existing', force=True)
        mock_client.call.assert_called_with('DELETE', '/api/v1/domains/u-vm-existing?storage=remove')

    @patch('virtomir_machine.main')
    def test_machine_alias_module(self, mock_main):
        virtomir_machine.main()
        mock_main.assert_called_once()


class TestDiscovery(unittest.TestCase):
    def test_discovery_under_tests_directory(self):
        tests_dir = os.path.abspath(os.path.join(CUR_DIR, '..'))
        loader = unittest.TestLoader()
        suite = loader.discover(tests_dir)
        self.assertGreater(suite.countTestCases(), 0)


if __name__ == '__main__':
    unittest.main()
