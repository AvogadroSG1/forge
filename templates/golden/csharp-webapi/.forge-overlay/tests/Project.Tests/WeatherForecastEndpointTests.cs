// <copyright file="WeatherForecastEndpointTests.cs" company="{{.AuthorName}}">
// Copyright (c) {{.AuthorName}}. All rights reserved.
// </copyright>

using System.Diagnostics;
using System.Net;
using System.Net.Sockets;
using System.Text;
using Xunit;

/// <summary>
/// Verifies the scaffolded weather forecast endpoint responds successfully.
/// </summary>
public sealed class WeatherForecastEndpointTests
{
    private static readonly object OutputSync = new();

    /// <summary>
    /// Ensures the weather forecast endpoint returns HTTP 200 from the real app process.
    /// </summary>
    /// <returns>A task that completes when the assertion finishes.</returns>
    [Fact]
    public async Task WeatherForecastEndpointReturnsOk()
    {
        var port = ReserveLoopbackPort();
        var projectRoot = FindProjectRoot();
        var output = new StringBuilder();

        using var process = StartApplicationProcess(projectRoot, port, output);
        using var handler = new SocketsHttpHandler
        {
            UseCookies = false,
            UseProxy = false,
        };
        using var client = new HttpClient(handler)
        {
            Timeout = TimeSpan.FromSeconds(2),
        };
        using var cancellationSource = new CancellationTokenSource(TimeSpan.FromSeconds(30));
        var cancellationToken = cancellationSource.Token;
        var endpoint = $"http://127.0.0.1:{port}/Weatherforecast";
        string? lastProbe = null;

        try
        {
            while (!cancellationToken.IsCancellationRequested)
            {
                try
                {
                    using var response = await client.GetAsync(endpoint, cancellationToken);
                    var content = await response.Content.ReadAsStringAsync(cancellationToken);

                    Assert.Equal(HttpStatusCode.OK, response.StatusCode);
                    Assert.Contains("temperatureC", content, StringComparison.OrdinalIgnoreCase);
                    return;
                }
                catch (OperationCanceledException) when (!cancellationToken.IsCancellationRequested)
                {
                    lastProbe = "request timed out";
                }
                catch (HttpRequestException ex)
                {
                    lastProbe = ex.Message;
                }

                try
                {
                    await Task.Delay(500, cancellationToken);
                }
                catch (OperationCanceledException)
                {
                    break;
                }
            }
        }
        finally
        {
            if (!process.HasExited)
            {
                process.Kill(entireProcessTree: true);
                await process.WaitForExitAsync(TestContext.Current.CancellationToken);
            }
        }

        Assert.Fail(
            $"Weather forecast endpoint did not become ready. Last probe: {lastProbe}{Environment.NewLine}--- output ---{Environment.NewLine}{output}");
    }

    private static string FindProjectRoot()
    {
        return Path.GetFullPath(Path.Combine(AppContext.BaseDirectory, "..", "..", "..", "..", ".."));
    }

    private static int ReserveLoopbackPort()
    {
        var listener = new TcpListener(IPAddress.Loopback, 0);
        listener.Start();

        try
        {
            return ((IPEndPoint)listener.LocalEndpoint).Port;
        }
        finally
        {
            listener.Stop();
        }
    }

    private static Process StartApplicationProcess(string projectRoot, int port, StringBuilder output)
    {
        var process = new Process
        {
            StartInfo = new ProcessStartInfo("dotnet")
            {
                WorkingDirectory = projectRoot,
                RedirectStandardOutput = true,
                RedirectStandardError = true,
                UseShellExecute = false,
            },
        };

        process.StartInfo.ArgumentList.Add("run");
        process.StartInfo.ArgumentList.Add("--no-build");
        process.StartInfo.ArgumentList.Add("--no-launch-profile");
        process.StartInfo.Environment["ASPNETCORE_URLS"] = $"http://127.0.0.1:{port}";
        process.OutputDataReceived += (_, args) => AppendOutput(output, args.Data);
        process.ErrorDataReceived += (_, args) => AppendOutput(output, args.Data);

        if (!process.Start())
        {
            throw new InvalidOperationException("Failed to start dotnet run for the scaffolded web API.");
        }

        process.BeginOutputReadLine();
        process.BeginErrorReadLine();
        return process;
    }

    private static void AppendOutput(StringBuilder output, string? line)
    {
        if (string.IsNullOrWhiteSpace(line))
        {
            return;
        }

        lock (OutputSync)
        {
            _ = output.AppendLine(line);
        }
    }
}
