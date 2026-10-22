//! The remote flows the menu runs (#156), with the prompts injected so they
//! run headless in tests: adding a connection (discover, choose daemons,
//! store tokens), re-reading a token, and the tunnels a connection list
//! needs.

use std::collections::HashSet;

use crate::remote::{
    create_remote_hub_token, daemons_from_records, discover, discover_hub, hub_key,
    read_remote_token, token_key, validate_api_token, Connection, HubListing, RemoteDaemon,
    Remotes, Transport,
};
use crate::shellpath::Tools;
use crate::status::DaemonRecord;
use crate::token::TokenStore;
use crate::tunnel::{TunnelSpec, DAEMON_HEALTH_PATH, HUB_HEALTH_PATH};

/// What an add produced.
#[derive(Debug)]
pub struct Added {
    pub connection: Connection,
    /// Per daemon key, why no token is stored (it shows "token required"
    /// and can be entered from the menu).
    pub token_problems: Vec<(String, String)>,
}

/// Obtains a daemon's token: its `token_file` read over the transport when
/// it has one, else (or when that fails) `ask` — the user typing it. Stored
/// under the connection+daemon key. `Ok(false)` when there is none to store
/// (an open API, or the user declined).
pub fn obtain_token(
    conn: &Connection,
    daemon: &RemoteDaemon,
    tools: &Tools,
    tokens: &dyn TokenStore,
    ask: &mut dyn FnMut(&RemoteDaemon, Option<&str>) -> Option<String>,
) -> Result<bool, String> {
    if !daemon.token_required {
        return Ok(false);
    }
    let key = token_key(&conn.id, &daemon.workflow);
    let read = if daemon.token_file.is_empty() {
        Err(String::new())
    } else {
        read_remote_token(conn, daemon, tools)
    };
    let tok = match read {
        Ok(t) => t,
        Err(why) => {
            let why = (!why.is_empty()).then_some(why);
            match ask(daemon, why.as_deref()) {
                Some(t) => {
                    validate_api_token(&t)?;
                    t
                }
                None => return why.map_or(Ok(false), Err),
            }
        }
    };
    tokens.set(&key, &tok)?;
    Ok(true)
}

/// What the user entered for a new connection.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct AddRequest {
    pub name: String,
    pub transport: Transport,
    /// How to run itervox on the host; `None` for plain `itervox`.
    pub itervox_command: Option<String>,
}

/// "Add remote": discovers the daemons on the host, lets `choose` pick
/// which to add (indices into the listing; all when it returns `None`), and
/// stores a token for each. The connection is returned for the caller to
/// save; nothing is saved here.
pub fn add_connection(
    remotes: &Remotes,
    req: AddRequest,
    tools: &Tools,
    tokens: &dyn TokenStore,
    choose: &mut dyn FnMut(&[DaemonRecord]) -> Option<Vec<usize>>,
    ask_token: &mut dyn FnMut(&RemoteDaemon, Option<&str>) -> Option<String>,
) -> Result<Added, String> {
    let AddRequest {
        name,
        transport,
        itervox_command,
    } = req;
    transport.validate()?;
    let name = if name.trim().is_empty() {
        transport.label()
    } else {
        name.trim().to_string()
    };
    let id = crate::remote::connection_id(&name, &remotes.ids());
    let mut conn = Connection::new(id, name, transport);
    if let Some(c) = itervox_command.filter(|c| !c.trim().is_empty()) {
        conn.itervox_command = c.trim().to_string();
    }
    let records = discover(&conn, tools)?;
    let all = daemons_from_records(&records);
    if all.is_empty() {
        return Err(format!(
            "no running itervox daemon was found on {}",
            conn.transport.label()
        ));
    }
    // Choose among the reachable daemons, in listing order.
    let shown: Vec<DaemonRecord> = all
        .iter()
        .filter_map(|d| records.iter().find(|r| r.workflow == d.workflow).cloned())
        .collect();
    let picked = if all.len() == 1 {
        vec![0]
    } else {
        choose(&shown).unwrap_or_else(|| (0..all.len()).collect())
    };
    let chosen: Vec<RemoteDaemon> = picked
        .into_iter()
        .filter_map(|i| all.get(i).cloned())
        .collect();
    if chosen.is_empty() {
        return Err("no daemon chosen".into());
    }
    conn.set_daemons(chosen);
    let mut token_problems = Vec::new();
    // #232: the host's hub, when it has one, covers its member daemons
    // through one tunnel. Each daemon's own token is still stored: it is
    // read directly again whenever the hub stops.
    let listing = discover_hub(&conn, tools);
    if let Err(e) = attach_hub(&mut conn, listing, tools, tokens) {
        token_problems.push((hub_key(&conn.id), e));
    }
    for d in conn.daemons.clone() {
        match obtain_token(&conn, &d, tools, tokens, ask_token) {
            Ok(_) => {}
            Err(e) => token_problems.push((token_key(&conn.id, &d.workflow), e)),
        }
    }
    Ok(Added {
        connection: conn,
        token_problems,
    })
}

/// Lists the host's daemons again: new ones are added (with tokens), gone
/// ones dropped, known ones keep their local port.
pub fn rediscover(
    conn: &mut Connection,
    tools: &Tools,
    tokens: &dyn TokenStore,
    ask_token: &mut dyn FnMut(&RemoteDaemon, Option<&str>) -> Option<String>,
) -> Result<Vec<(String, String)>, String> {
    let records = discover(conn, tools)?;
    let before: Vec<String> = conn.daemons.iter().map(|d| d.workflow.clone()).collect();
    conn.set_daemons(daemons_from_records(&records));
    let mut problems = Vec::new();
    let listing = discover_hub(conn, tools);
    if let Err(e) = attach_hub(conn, listing, tools, tokens) {
        problems.push((hub_key(&conn.id), e));
    }
    for d in conn.daemons.clone() {
        let key = token_key(&conn.id, &d.workflow);
        let known = before.contains(&d.workflow) && tokens.get(&key).ok().flatten().is_some();
        if !known {
            if let Err(e) = obtain_token(conn, &d, tools, tokens, ask_token) {
                problems.push((key, e));
            }
        }
    }
    for wf in before {
        if !conn.daemons.iter().any(|d| d.workflow == wf) {
            let _ = tokens.delete(&token_key(&conn.id, &wf));
        }
    }
    Ok(problems)
}

/// Sets `conn.hub` from the host's hub listing (#232) and makes sure the
/// app holds a hub token for it (created on the host on first use). No
/// listing, no hub, no member among the connection's daemons, or no token:
/// no hub, and every daemon keeps its own tunnel. The local port of a hub
/// already known is kept.
pub fn attach_hub(
    conn: &mut Connection,
    listing: Option<HubListing>,
    tools: &Tools,
    tokens: &dyn TokenStore,
) -> Result<(), String> {
    let old_port = conn.hub.as_ref().map_or(0, |h| h.local_port);
    let key = hub_key(&conn.id);
    let Some(mut hub) = listing.and_then(|l| l.hub_for(&conn.daemons)) else {
        conn.hub = None;
        return Ok(());
    };
    hub.local_port = old_port;
    if tokens.get(&key).ok().flatten().is_none() {
        match create_remote_hub_token(conn, tools) {
            Ok(t) => tokens.set(&key, &t)?,
            Err(e) => {
                conn.hub = None;
                return Err(e);
            }
        }
    }
    conn.hub = Some(hub);
    Ok(())
}

/// Forgets a connection's tokens (on remove), its hub's included.
pub fn forget_tokens(conn: &Connection, tokens: &dyn TokenStore) {
    for d in &conn.daemons {
        let _ = tokens.delete(&token_key(&conn.id, &d.workflow));
    }
    let _ = tokens.delete(&hub_key(&conn.id));
}

/// The tunnels a connection list needs while every remote hub serves: one
/// per hub, and one per daemon the hub does not cover.
pub fn tunnel_specs(remotes: &Remotes) -> Vec<TunnelSpec> {
    tunnel_specs_with(remotes, &HashSet::new())
}

/// The tunnels a connection list needs (#232): ONE per remote hub, covering
/// all of its members, and one per daemon that is not a member. For the
/// connections in `hub_down` (their hub stopped answering) every member
/// gets its own tunnel again, until the hub serves again; the hub's tunnel
/// keeps running so its return is noticed.
pub fn tunnel_specs_with(remotes: &Remotes, hub_down: &HashSet<String>) -> Vec<TunnelSpec> {
    let mut specs = Vec::new();
    for c in &remotes.connections {
        let down = hub_down.contains(&c.id);
        if let Some(h) = &c.hub {
            specs.push(TunnelSpec {
                id: hub_key(&c.id),
                transport: c.transport.clone(),
                daemon_host: h.forward_host(),
                remote_port: h.remote_port,
                preferred_port: h.local_port,
                health_path: HUB_HEALTH_PATH.into(),
            });
        }
        for d in &c.daemons {
            let covered = c
                .hub
                .as_ref()
                .is_some_and(|h| h.member(&d.workflow).is_some());
            if covered && !down {
                continue;
            }
            specs.push(TunnelSpec {
                id: token_key(&c.id, &d.workflow),
                transport: c.transport.clone(),
                daemon_host: d.forward_host(),
                remote_port: d.remote_port,
                preferred_port: d.local_port,
                health_path: DAEMON_HEALTH_PATH.into(),
            });
        }
    }
    specs
}

#[cfg(test)]
mod tests {
    use super::*;

    /// #232: a known hub keeps its local port and the stored hub token (no
    /// command runs on the host); a host without a hub, or whose hub serves
    /// none of the chosen daemons, keeps a tunnel per daemon.
    #[test]
    fn attach_hub_keeps_the_port_and_stored_token() {
        use crate::remote::{HubListing, RemoteHub, RemoteHubMember};
        use crate::token::MemoryTokenStore;
        let tools = Tools {
            path_env: std::ffi::OsString::from("/nonexistent"),
            ssh: None,
            gcloud: None,
            path_source: "inherited",
        };
        let mut c = Connection::new(
            "prod".into(),
            "Prod".into(),
            Transport::Ssh {
                destination: "me@box".into(),
            },
        );
        c.daemons.push(RemoteDaemon {
            workflow: "/srv/a/WORKFLOW.md".into(),
            remote_host: String::new(),
            remote_port: 8090,
            token_required: true,
            token_source: "generated".into(),
            token_file: String::new(),
            local_port: 0,
        });
        c.hub = Some(RemoteHub {
            remote_host: "127.0.0.1".into(),
            remote_port: 8089,
            local_port: 50200,
            members: vec![],
        });
        let tokens = MemoryTokenStore::default();
        tokens.set(&hub_key("prod"), "stored").unwrap();
        let listing: HubListing = serde_json::from_str(
            r#"{"hub":{"pid":1,"host":"127.0.0.1","port":8089},"members":[{"name":"a","workflow":"/srv/a/WORKFLOW.md"},{"name":"x","workflow":"/srv/x/WORKFLOW.md"}]}"#,
        )
        .unwrap();
        attach_hub(&mut c, Some(listing.clone()), &tools, &tokens).unwrap();
        let hub = c.hub.clone().unwrap();
        assert_eq!(hub.local_port, 50200);
        assert_eq!(
            hub.members,
            [RemoteHubMember {
                name: "a".into(),
                workflow: "/srv/a/WORKFLOW.md".into()
            }]
        );
        assert_eq!(
            tunnel_specs(&Remotes {
                connections: vec![c.clone()]
            })
            .len(),
            1
        );

        // An open-API daemon is never proxied: no hub for it.
        c.daemons[0].token_required = false;
        attach_hub(&mut c, Some(listing), &tools, &tokens).unwrap();
        assert_eq!(c.hub, None);
        attach_hub(&mut c, None, &tools, &tokens).unwrap();
        assert_eq!(c.hub, None);
        assert_eq!(
            tunnel_specs(&Remotes {
                connections: vec![c]
            })
            .len(),
            1
        );
    }

    #[test]
    fn specs_carry_transport_ports_and_keys() {
        let mut c = Connection::new(
            "prod".into(),
            "Prod".into(),
            Transport::Ssh {
                destination: "me@box".into(),
            },
        );
        c.daemons.push(RemoteDaemon {
            workflow: "/srv/a/WORKFLOW.md".into(),
            remote_host: "0.0.0.0".into(),
            remote_port: 8091,
            token_required: false,
            token_source: String::new(),
            token_file: String::new(),
            local_port: 50100,
        });
        let specs = tunnel_specs(&Remotes {
            connections: vec![c],
        });
        assert_eq!(specs.len(), 1);
        assert_eq!(specs[0].id, "remote/prod//srv/a/WORKFLOW.md");
        assert_eq!(specs[0].daemon_host, "127.0.0.1");
        assert_eq!(specs[0].remote_port, 8091);
        assert_eq!(specs[0].preferred_port, 50100);
    }
}
