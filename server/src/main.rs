use std::process::ExitCode;

use clap::Parser;
use monty_server::{
    app::{self, StartOptions},
    config::{Cli, Command},
    probe,
    telemetry::Telemetry,
};

fn main() -> ExitCode {
    let cli = Cli::parse();
    if let Some(Command::Probe { url }) = &cli.command {
        return probe::probe(url.as_deref());
    }
    let config = match cli.serve.validate() {
        Ok(config) => config,
        Err(err) => {
            eprintln!("monty-server: {err}");
            return ExitCode::from(2);
        }
    };
    let telemetry = match &config.otlp {
        None => None,
        Some(otlp) => match Telemetry::install(otlp) {
            Ok(telemetry) => Some(telemetry),
            Err(err) => {
                eprintln!("monty-server: {err}");
                return ExitCode::from(2);
            }
        },
    };
    let runtime = match tokio::runtime::Builder::new_multi_thread().enable_all().build() {
        Ok(runtime) => runtime,
        Err(err) => {
            eprintln!("monty-server: runtime: {err}");
            return ExitCode::from(2);
        }
    };
    let options = StartOptions {
        print_url: true,
        watch_signals: true,
    };
    let code = runtime.block_on(async {
        match app::start(config, telemetry.clone(), options).await {
            Ok(server) => match server.wait().await {
                Ok(()) => ExitCode::SUCCESS,
                Err(err) => {
                    eprintln!("monty-server: {err}");
                    ExitCode::FAILURE
                }
            },
            Err(err) => {
                eprintln!("monty-server: {err}");
                ExitCode::from(2)
            }
        }
    });
    drop(runtime);
    drop(telemetry);
    code
}
