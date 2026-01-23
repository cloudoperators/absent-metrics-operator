// SPDX-FileCopyrightText: 2020 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	_ "go.uber.org/automaxprocs"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	"github.com/sapcc/go-api-declarations/bininfo"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/sapcc/absent-metrics-operator/controllers"
	//+kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(monitoringv1.AddToScheme(scheme))

	//+kubebuilder:scaffold:scheme
}

func main() {
	var (
		debug                bool
		metricsAddr          string
		probeAddr            string
		enableLeaderElection bool
		keepLabel            labelsMap
		prometheusRuleName   string
	)
	bininfo.HandleVersionArgument()

	flag.BoolVar(&debug, "debug", false, "Alias for '-zap-devel' flag.")
	// Port `9659` has been allocated for absent metrics operator: https://github.com/prometheus/prometheus/wiki/Default-port-allocations
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":9659", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.StringVar(&prometheusRuleName, "prom-rule-name", controllers.DefaultAbsencePromRuleNameTemplate,
		"The template to be used as the name of generated PrometheusRule(s) and consequently aggregating generated absence alert rules.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.Var(&keepLabel, "keep-labels", "A comma-separated list of labels to retain from the original alert rule. "+
		fmt.Sprintf("(default '%s,%s,%s')", controllers.LabelSupportGroup, controllers.LabelTier, controllers.LabelService))
	opts := zap.Options{}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	// Enable debug mode if `-debug` flag is provided.
	if debug {
		opts.Development = true
	}
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// Set default value for '-keep-labels' flag.
	if len(keepLabel) == 0 {
		keepLabel = labelsMap{
			controllers.LabelSupportGroup: true,
			controllers.LabelTier:         true,
			controllers.LabelService:      true,
		}
	}

	prometheusRuleNameGen, err := controllers.CreateAbsencePromRuleNameGenerator(prometheusRuleName)
	if err != nil {
		setupLog.Error(err, "unable to parse PrometheusRule name template", "prom-rule-name", prometheusRuleName)
		os.Exit(1)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "absent-metrics-operator.cloud.sap",
		Controller:             config.Controller{MaxConcurrentReconciles: 8}})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	controllers.RegisterMetrics()

	if err = (&controllers.PrometheusRuleReconciler{
		Client:             mgr.GetClient(),
		Scheme:             mgr.GetScheme(),
		Log:                ctrl.Log.WithName("controller").WithName("prometheusrule"),
		KeepLabel:          controllers.KeepLabel(keepLabel),
		PrometheusRuleName: prometheusRuleNameGen,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "PrometheusRule")
		os.Exit(1)
	}
	//+kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	version := bininfo.VersionOr("dev")
	commit := bininfo.CommitOr("unknown")
	date := bininfo.BuildDateOr("now")
	setupLog.Info("starting manager", "version", version, "git-commit", commit, "build-date", date)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

// labelsMap type is a wrapper around controllers.KeepLabel. It is used for the
// `--keep-labels` flag to convert a comma-separated string into a map.
type labelsMap controllers.KeepLabel

// String implements the flag.Value interface.
func (lm labelsMap) String() string {
	list := make([]string, 0, len(lm))
	for k := range lm {
		list = append(list, k)
	}
	return strings.Join(list, ",")
}

// Set implements the flag.Value interface.
func (lm *labelsMap) Set(in string) error {
	labels := make(labelsMap)
	for v := range strings.SplitSeq(in, ",") {
		labels[strings.TrimSpace(v)] = true
	}

	*lm = labels
	return nil
}
