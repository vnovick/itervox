//! The remote flows the menu runs (#156), with the prompts injected so they
//! run headless in tests: adding a connection (discover, choose daemons,
//! store tokens), re-reading a token, and the tunnels a connection list
//! needs.

use crate::remote::{
    daemons_from_records, discover, read_remote_token, token_key, validate_api_token, Connection,
    RemoteDaemon, Remotes, Transport,
};
use crate::shellpath::Tools;
use crate::status::DaemonRecord;
use crate::token::TokenStore;
use crate::tunnel::TunnelSpec;

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

/// Forgets a connection's tokens (on remove).
pub fn forget_tokens(conn: &Connection, tokens: &dyn TokenStore) {
    for d in &conn.daemons {
        let _ = tokens.delete(&token_key(&conn.id, &d.workflow));
    }
}

/// One tunnel per remote daemon.
pub fn tunnel_specs(remotes: &Remotes) -> Vec<TunnelSpec> {
    remotes
        .connections
        .iter()
        .flat_map(|c| {
            c.daemons.iter().map(move |d| TunnelSpec {
                id: token_key(&c.id, &d.workflow),
                transport: c.transport.clone(),
                daemon_host: d.forward_host(),
                remote_port: d.remote_port,
                preferred_port: d.local_port,
            })
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

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
